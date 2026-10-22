import Foundation
import SwiftUI

struct CloudControlEnvelope: Codable, Equatable, Sendable {
    struct Input: Codable, Equatable, Sendable {
        var type: String
        var id: String
        var text: String? = nil
        var allow: Bool? = nil
        var decision: String? = nil
        var answers: [String: [String]]? = nil
    }
    var id: String
    var approvalSeq: Int64? = nil
    var control: Input
    var valid: Bool {
        guard UUID(uuidString: id) != nil else { return false }
        if control.type == "steer" {
            return approvalSeq == nil && control.id == id && control.allow == nil && control.decision == nil && control.answers == nil &&
                !(control.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && (control.text?.utf8.count ?? 0) <= 65536
        }
        return control.type == "approval" && (approvalSeq ?? 0) > 0 && UUID(uuidString:control.id) != nil && control.allow != nil && control.text == nil
    }
}
struct CloudControlStatus: Decodable, Sendable {
    var id: String
    var requestId: String
    var kind: String
    var state: String
}
extension CloudOutboxItem {
    var controlRequestID: String? {
        let parts = path.split(separator: "/").map(String.init)
        guard method == "POST", parts.count == 6, parts[0] == "v1", parts[1] == "camps", UUID(uuidString: parts[2]) != nil,
              parts[3] == "requests", UUID(uuidString: parts[4]) != nil, parts[5] == "controls" else { return nil }
        return parts[4]
    }
    var cloudControl: CloudControlEnvelope? {
        guard controlRequestID != nil, UUID(uuidString: sessionID) != nil, body.count <= 96 << 10,
              let value = try? CampWire.decoder().decode(CloudControlEnvelope.self, from: body), value.id == id, value.valid else { return nil }
        return value
    }
    var controlPending: Bool { cloudControl != nil && ["pending", "sending", "waiting"].contains(state) }
}

struct CloudControlQueueView: View {
    @ObservedObject var cloud: CampConnection
    var sessionID: String
    var requestID: String? = nil
    var restore: (String) -> Void
    var items: [CloudOutboxItem] { cloud.pending.filter { item in item.sessionID == sessionID && item.cloudControl != nil && (requestID.map { item.controlRequestID == $0 } ?? !cloud.requests.contains { $0.isIsolatedWork && $0.id == item.controlRequestID }) } }
    var body: some View {
        if !items.isEmpty {
        ScrollView {
        VStack(alignment:.leading,spacing:8) {
        ForEach(items) { item in
            if let value = item.cloudControl {
                HStack(spacing: 8) {
                    Image(systemName: item.controlPending ? "clock" : "exclamationmark.circle")
                    Text(item.controlPending ? (item.state == "waiting" ? "Waiting for the provider to accept your \(value.control.type == "steer" ? "direction" : "answer")" : "Saved on this Mac · Waiting to send") : "\(item.error) · Check the transcript")
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 4)
                    if !item.controlPending {
                        if let text = value.control.text {
                            Button("Restore prompt") { Task { if await cloud.discardControl(item) { restore(text) } } }
                        } else {
                            Button("Dismiss") { Task { _ = await cloud.discardControl(item) } }
                        }
                    }
                }.font(.system(size: 11)).foregroundStyle(campMuted)
            }
        }
        }
        }.frame(height:min(CGFloat(items.count)*44,120))
        }
    }
}
