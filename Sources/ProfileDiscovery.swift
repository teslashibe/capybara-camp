import Foundation

struct ProfileDiscovery: Decodable {
    let sources: [CampSource]
}

extension CampStore {
    func applyProfileDiscovery(_ found: [CampSource]) {
        // A fast directory check has no message counts and cannot prove that
        // an old source was removed. The completed import owns reconciliation.
        var merged: [String: CampSource] = [:]
        for source in sources { merged[source.id] = source }
        for source in found where merged[source.id] == nil { merged[source.id] = source }
        sources = merged.values.sorted { ($0.provider, $0.label, $0.id) < ($1.provider, $1.label, $1.id) }
    }
}
