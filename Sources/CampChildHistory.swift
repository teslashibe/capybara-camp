import Foundation

struct LocalChildHistorySource: Codable, Sendable {
    var id: String; var localID: String; var name: String; var phase: String; var workflow: String
    var status: String; var model: String; var revision: String
    var valid: Bool { [id,revision].allSatisfy{ $0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil } }
}
struct LocalChildHistoryCatalog: Decodable, Sendable {
    var parentID: String; var revision: String; var sources: [LocalChildHistorySource]
    var valid: Bool {
        [parentID,revision].allSatisfy{$0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil} && sources.count<=512 &&
        sources.allSatisfy(\.valid) && Set(sources.map(\.id)).count==sources.count
    }
}
struct LocalChildHistoryPage: Decodable, Sendable {
    var parentID: String; var source: LocalChildHistorySource; var page: TranscriptExport
}
struct CloudChildHistory: Codable, Equatable, Identifiable, Sendable {
    var id: String; var parentId: String; var sourceKey: String; var sourceRevision: String
    var name: String; var phase: String; var workflow: String; var state: String; var model: String
    var complete: Bool; var removed: Bool; var revision: Int64; var transcriptRevision: Int64
    func valid(parent:String)->Bool {
        UUID(uuidString:id) != nil && parentId==parent && revision>0 && transcriptRevision>=0 &&
        [sourceKey,sourceRevision].allSatisfy{$0.range(of:"^[a-f0-9]{64}$",options:.regularExpression) != nil} &&
        !name.isEmpty && name.utf8.count<=500 && phase.utf8.count<=128 && workflow.utf8.count<=256 && model.utf8.count<=200 &&
        ["unknown","running","done","failed","stopped","cancelled"].contains(state)
    }
    var childSession:ChildSession {
        displaySession(cached:false)
    }
    func displaySession(cached:Bool)->ChildSession {
        ChildSession(id:id,sessionID:id,name:name,phase:phase,workflow:workflow,status:cached ? "saved" : complete ? state : "syncing",model:model,path:"",updated:Double(revision))
    }
}
struct CloudChildHistoryList: Codable, Sendable {
    var parentRevision: Int64; var permissionRevision: Int64; var children: [CloudChildHistory]
    var cachedAt:Date? = nil
    var displayChildren:[ChildSession] {
        children.filter{!$0.removed}.map{$0.displaySession(cached:cachedAt != nil)}
    }
    func valid(parent:String)->Bool {
        parentRevision>=0 && permissionRevision>0 && children.count<=512 && children.allSatisfy{$0.valid(parent:parent) && $0.revision<=parentRevision} &&
        Set(children.map(\.id)).count==children.count && Set(children.map(\.sourceKey)).count==children.count
    }
}
struct CloudChildTranscript: Codable, Sendable {
    var parentRevision: Int64; var permissionRevision: Int64; var child: CloudChildHistory; var transcript: CloudTranscript
    var cachedAt:Date? = nil
    func valid(parent:String,before:Int64)->Bool {
        let floor=transcript.hasMore ? transcript.before : 0,ceiling=before==0 ? Int64.max : before
        return parentRevision>0 && permissionRevision>0 && child.valid(parent:parent) && child.complete && !child.removed &&
        child.revision<=parentRevision && transcript.revision==child.transcriptRevision && transcript.messages.count<=120 &&
        before>=0 && floor>=0 && floor<=ceiling && (!transcript.hasMore || !transcript.messages.isEmpty) &&
        transcript.messages.allSatisfy{$0.ordinal>=floor && $0.ordinal<ceiling && $0.revision<=transcript.revision} &&
        Set(transcript.messages.map(\.id)).count==transcript.messages.count && Set(transcript.messages.map(\.ordinal)).count==transcript.messages.count
    }
}

func childHistoryTransportUnavailable(_ error:Error)->Bool {
    guard let error=error as? URLError else{return false}
    return [.notConnectedToInternet,.networkConnectionLost,.cannotConnectToHost,.cannotFindHost,.dnsLookupFailed,.timedOut].contains(error.code)
}
func childHistoryAccessDenied(_ error:Error)->Bool {
    if case CampCloudError.server(let code,_) = error {return [401,403,404,410].contains(code)}
    return false
}

enum ChildHistoryBridge {
    static func catalog(_ session:CampSession) async throws->LocalChildHistoryCatalog {
        let raw=try await Bridge.call("child-history-catalog",input:CampWire.body(["session":JSONSerialization.jsonObject(with:JSONEncoder().encode(session))]))
        let value=try JSONDecoder().decode(LocalChildHistoryCatalog.self,from:raw)
        guard value.valid else{throw CampCloudError.invalidResponse};return value
    }
    static func page(_ session:CampSession,_ source:LocalChildHistorySource,_ after:Int64) async throws->LocalChildHistoryPage {
        let raw=try await Bridge.call("child-export-page",input:CampWire.body(["session":JSONSerialization.jsonObject(with:JSONEncoder().encode(session)),"sourceID":source.id,"revision":source.revision,"after":after,"limit":200]))
        return try JSONDecoder().decode(LocalChildHistoryPage.self,from:raw)
    }
}

extension CampConnection {
    func childHistories(session:CloudSession) async throws->CloudChildHistoryList {
        guard let scope,let client,let credential,let cache else{throw CampCloudError.cacheUnavailable}
        let epoch=try await cache.epoch(scope)
        let historyEpoch=try await cache.childEpoch(scope,parent:session.id)
        let value:CloudChildHistoryList
        do {value=try await client.get(CloudChildHistoryList.self,path:"/v1/camps/\(scope.campId)/sessions/\(session.id)/children",token:credential.token)}
        catch {
            if childHistoryAccessDenied(error) {try? await cache.invalidateChildHistory(scope:scope,parent:session.id,expectedEpoch:epoch,expectedChildEpoch:historyEpoch)}
            guard childHistoryTransportUnavailable(error),self.scope==scope,self.credential?.token==credential.token,childReadAllowed(session),!Task.isCancelled,
                  try await cache.epoch(scope)==epoch,let saved=try await cache.childCatalog(scope,parent:session.id,permission:session.permissionRevision) else{throw error}
            guard self.scope==scope,self.credential?.token==credential.token,childReadAllowed(session),!Task.isCancelled,try await cache.epoch(scope)==epoch,try await cache.childEpoch(scope,parent:session.id)==historyEpoch else{throw CancellationError()}
            return saved
        }
        guard self.scope==scope,self.credential?.token==credential.token,try await cache.epoch(scope)==epoch,try await cache.childEpoch(scope,parent:session.id)==historyEpoch,!Task.isCancelled else{throw CancellationError()}
        guard childReadAllowed(session),value.valid(parent:session.id),value.permissionRevision==session.permissionRevision else{throw CampCloudError.invalidResponse}
        try await cache.saveChildCatalog(value,scope:scope,parent:session.id,expectedEpoch:epoch,expectedChildEpoch:historyEpoch)
        guard self.scope==scope,self.credential?.token==credential.token,childReadAllowed(session),!Task.isCancelled,try await cache.epoch(scope)==epoch,try await cache.childEpoch(scope,parent:session.id)==historyEpoch else{throw CancellationError()}
        return value
    }
    func childTranscript(session:CloudSession,childID:String,before:Int64=0,revision:Int64=0) async throws->CloudChildTranscript {
        try await readChildResponse(session:session,childID:childID,before:before,revision:revision,messageID:nil)
    }
    func childMessage(session:CloudSession,childID:String,revision:Int64,messageID:String)async throws->CloudChildTranscript {
        guard revision>0,!messageID.isEmpty,messageID.utf8.count<=256 else{throw CampCloudError.invalidResponse}
        return try await readChildResponse(session:session,childID:childID,before:0,revision:revision,messageID:messageID)
    }
    private func readChildResponse(session:CloudSession,childID:String,before:Int64,revision:Int64,messageID:String?)async throws->CloudChildTranscript {
        guard UUID(uuidString:childID) != nil,before>=0,revision>=0,let scope,let client,let credential,let cache else{throw CampCloudError.invalidResponse}
        let epoch=try await cache.epoch(scope)
        let historyEpoch=try await cache.childEpoch(scope,parent:session.id)
        let path="/v1/camps/\(scope.campId)/sessions/\(session.id)/children/\(childID)"
        let raw:Data
        do {
            if let messageID {raw=try await client.data("POST",path:path+"/message",body:CampWire.body(["revision":revision,"message_id":messageID]),token:credential.token)}
            else {raw=try await client.data("GET",path:path+"/transcript?limit=120&before=\(before)&revision=\(revision)",token:credential.token)}
        } catch {
            if childHistoryAccessDenied(error) {try? await cache.invalidateChildHistory(scope:scope,parent:session.id,expectedEpoch:epoch,expectedChildEpoch:historyEpoch)}
            guard messageID==nil,childHistoryTransportUnavailable(error),self.scope==scope,self.credential?.token==credential.token,childReadAllowed(session),!Task.isCancelled,
                  try await cache.epoch(scope)==epoch,let saved=try await cache.childPage(scope,parent:session.id,child:childID,permission:session.permissionRevision,before:before,revision:revision) else{throw error}
            guard self.scope==scope,self.credential?.token==credential.token,childReadAllowed(session),!Task.isCancelled,try await cache.epoch(scope)==epoch,try await cache.childEpoch(scope,parent:session.id)==historyEpoch else{throw CancellationError()}
            return saved
        }
        let value=try CampWire.decoder().decode(CloudChildTranscript.self,from:raw)
        guard self.scope==scope,self.credential?.token==credential.token,try await cache.epoch(scope)==epoch,try await cache.childEpoch(scope,parent:session.id)==historyEpoch,!Task.isCancelled else{throw CancellationError()}
        guard childReadAllowed(session),value.permissionRevision==session.permissionRevision,value.child.valid(parent:session.id),value.child.id==childID,
              value.child.complete,!value.child.removed,value.child.revision<=value.parentRevision,
              revision==0 || value.parentRevision==revision,value.transcript.revision==value.child.transcriptRevision,
              value.transcript.messages.count<=120 else{throw CampCloudError.invalidResponse}
        if let messageID {guard value.transcript.messages.count==1,value.transcript.messages[0].id==messageID else{throw CampCloudError.invalidResponse}}
        else {
            guard value.valid(parent:session.id,before:before) else{throw CampCloudError.invalidResponse}
            try await cache.saveChildPage(value,scope:scope,parent:session.id,before:before,expectedEpoch:epoch,expectedChildEpoch:historyEpoch)
        }
        guard self.scope==scope,self.credential?.token==credential.token,childReadAllowed(session),!Task.isCancelled,try await cache.epoch(scope)==epoch,try await cache.childEpoch(scope,parent:session.id)==historyEpoch else{throw CancellationError()}
        return value
    }
}
