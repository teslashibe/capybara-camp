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
        ChildSession(id:id,sessionID:id,name:name,phase:phase,workflow:workflow,status:complete ? state : "syncing",model:model,path:"",updated:Double(revision))
    }
}
struct CloudChildHistoryList: Codable, Sendable {
    var parentRevision: Int64; var permissionRevision: Int64; var children: [CloudChildHistory]
    func valid(parent:String)->Bool {
        parentRevision>=0 && permissionRevision>0 && children.count<=512 && children.allSatisfy{$0.valid(parent:parent) && $0.revision<=parentRevision} &&
        Set(children.map(\.id)).count==children.count && Set(children.map(\.sourceKey)).count==children.count
    }
}
struct CloudChildTranscript: Decodable, Sendable {
    var parentRevision: Int64; var permissionRevision: Int64; var child: CloudChildHistory; var transcript: CloudTranscript
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
        let value=try await client.get(CloudChildHistoryList.self,path:"/v1/camps/\(scope.campId)/sessions/\(session.id)/children",token:credential.token)
        guard self.scope==scope,self.credential?.token==credential.token,try await cache.epoch(scope)==epoch,!Task.isCancelled else{throw CancellationError()}
        guard childReadAllowed(session),value.valid(parent:session.id),value.permissionRevision==session.permissionRevision else{throw CampCloudError.invalidResponse}
        return value
    }
    func childTranscript(session:CloudSession,childID:String,before:Int64=0,revision:Int64=0) async throws->CloudChildTranscript {
        guard UUID(uuidString:childID) != nil,before>=0,revision>=0,let scope,let client,let credential,let cache else{throw CampCloudError.invalidResponse}
        let epoch=try await cache.epoch(scope)
        let value=try await client.get(CloudChildTranscript.self,path:"/v1/camps/\(scope.campId)/sessions/\(session.id)/children/\(childID)/transcript?limit=120&before=\(before)&revision=\(revision)",token:credential.token)
        guard self.scope==scope,self.credential?.token==credential.token,try await cache.epoch(scope)==epoch,!Task.isCancelled else{throw CancellationError()}
        guard childReadAllowed(session),value.permissionRevision==session.permissionRevision,value.child.valid(parent:session.id),value.child.id==childID,
              value.child.complete,!value.child.removed,value.child.revision<=value.parentRevision,
              revision==0 || value.parentRevision==revision,value.transcript.revision==value.child.transcriptRevision,
              value.transcript.messages.count<=120 else{throw CampCloudError.invalidResponse}
        return value
    }
}
