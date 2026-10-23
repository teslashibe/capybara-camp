import Foundation

// Synthetic local filesystem boundary; the API integration suite exercises real
// HTTP/PostgreSQL contracts without checking out a private GitHub repository.
actor DestinationBridgeFixture {
    let review: TransferCapture
    var checkoutCalls=0
    var installCalls=0
    var loseCheckout=false
    var loseInstall=false
    var delayInstall=false
    var installStarted=false
    init(_ review:TransferCapture) { self.review=review }
    func lose(_ phase:String) { loseCheckout=phase=="checkout";loseInstall=phase=="install" }
    func delay() { delayInstall=true }
    func started() -> Bool { installStarted }
    func counts() -> (Int,Int) { (checkoutCalls,installCalls) }
    func call(_ action:String,_ data:Data) async throws -> Data {
        if action=="transfer-review" { return try CampWire.encoder().encode(review) }
        let input=try JSONSerialization.jsonObject(with:data) as! [String:Any]
        if action=="transfer-checkout" {
            checkoutCalls+=1
            let scope=input["scope"] as! String,id=input["transferID"] as! String
            let receipt=TransferCheckoutReceipt(version:1,scope:scope,transferID:id,repository:input["repository"] as! String,baseCommit:review.baseCommit,branch:review.branch,destination:"/fixture/workspace-\(scope)-\(id)")
            if loseCheckout { loseCheckout=false;throw URLError(.networkConnectionLost) }
            return try JSONEncoder().encode(receipt)
        }
        precondition(action=="transfer-install")
        installCalls+=1;installStarted=true
        if delayInstall { try await Task.sleep(for:.milliseconds(180)) }
        let target=try JSONDecoder().decode(TransferInstallationTarget.self,from:JSONSerialization.data(withJSONObject:input["target"]!))
        let exported=try CampWire.decoder().decode(CloudWorkspaceExport.self,from:JSONSerialization.data(withJSONObject:input["export"]!))
        let receipt=TransferInstallationReceipt(version:1,scope:input["scope"] as! String,transferID:input["transferID"] as! String,
            exportID:exported.id,destination:input["destination"] as! String,target:target,
            payloadSHA256:input["payloadSHA256"] as! String,historySHA256:input["historySHA256"] as! String,
            sourceID:exported.sessionId,transcriptRevision:exported.transcriptRevision,
            files:.init(reviewSHA256:review.reviewSha256,baseCommit:review.baseCommit,branch:review.branch,files:review.files.count))
        let encoder=JSONEncoder();encoder.outputFormatting = .sortedKeys
        let result=TransferInstallationResult(receipt:receipt,receiptSHA256:PublishMessage.hash(try encoder.encode(receipt)))
        if loseInstall { loseInstall=false;throw URLError(.networkConnectionLost) }
        return try JSONEncoder().encode(result)
    }
}
