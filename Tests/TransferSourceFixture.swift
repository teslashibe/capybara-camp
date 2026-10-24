import Foundation

// Mock only the server side. The audit uses CampLocalHandoff and the bundled
// Go source guard to check the actual synthetic files and transcript revisions.
extension TransferFixture: CampPublishingTransport {
    func perform(_ operation: CampPublication) async throws -> Data {
        precondition(sourceWorker)
        let path=operation.path
        if path.hasSuffix("/recover") {
            guard let handoff else{return Data("[]".utf8)}
            return try CampWire.encoder().encode([CloudHandoffRecovery(handoff:handoff,session:session)])
        }
        if path.hasSuffix("/next") {
            guard let handoff,["pending","claimed"].contains(handoff.state) else{return Data()}
            return try CampWire.body(["id":handoff.id])
        }
        if operation.method=="GET" {
            if path.hasSuffix("/sessions/"+session.id){return try CampWire.encoder().encode(session)}
            guard let handoff,path.hasSuffix("/handoffs/"+handoff.id) else{throw CampCloudError.invalidResponse}
            return try CampWire.encoder().encode(handoff)
        }
        guard var h=handoff,let transfer,["pending","claimed","acknowledged","committed"].contains(h.state) else{throw CampCloudError.server(409,"invalid_transition")}
        let body=try JSONSerialization.jsonObject(with:operation.body) as! [String:Any]
        guard let claim=body["claim_id"] as? String else{throw CampCloudError.invalidResponse}
        if path.hasSuffix("/claim") {
            guard h.claimId==nil || h.claimId==claim else{throw CampCloudError.server(409,"claim_changed")}
            h.claimId=claim
            if h.state=="pending"{h.state="claimed"}
        } else {
            guard h.claimId==claim else{throw CampCloudError.server(409,"claim_changed")}
            if path.hasSuffix("/source") {
                let value=try CampWire.decoder().decode(CloudTransferSource.Transfer.self,from:CampWire.encoder().encode(transfer))
                return try CampWire.encoder().encode(CloudTransferSource(handoff:h,transfer:value,session:session))
            }
            if path.contains("/chunks/") {
                guard let ordinal=Int(path.split(separator:"/").last!),let chunk=chunks[ordinal] else{throw CampCloudError.invalidResponse}
                return try CampWire.body(["data":chunk.base64EncodedString(),"sha256":PublishMessage.hash(chunk)])
            }
            if path.hasSuffix("/ack") || path.hasSuffix("/finish") {
                guard body["stopped"] as? Bool==true,body["payload_sha256"] as? String==transfer.payloadSha256,
                      body["review_sha256"] as? String==transfer.reviewSha256 else{throw CampCloudError.invalidResponse}
                if path.hasSuffix("/finish") {
                    guard h.state=="acknowledged",let nonce=h.commitNonce,body["commit_nonce"] as? String==nonce else{throw CampCloudError.invalidResponse}
                    h.state="committed";h.executionEpoch=h.sourceEpoch+1
                    session.executionEpoch=h.executionEpoch;session.executionLocation="cloud";session.executionWorkspaceId=h.workspaceId
                    self.transfer?.state="completed"
                } else{h.state="acknowledged"}
            } else if path.hasSuffix("/reject"){h.state="invalidated"}
            else{throw CampCloudError.invalidResponse}
        }
        handoff=h
        return try CampWire.encoder().encode(h)
    }
}
