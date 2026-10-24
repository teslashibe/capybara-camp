import Foundation
import Security

protocol CampCredentialStore: Sendable {
    func read(origin: String) throws -> CloudCredential?
    func save(_ credential: CloudCredential) throws
    func remove(origin: String) throws
}

struct CampKeychain: CampCredentialStore {
    private let service = "com.capybaracamp.device-credentials"
    func read(origin: String) throws -> CloudCredential? {
        let query: [CFString: Any] = [kSecClass: kSecClassGenericPassword, kSecAttrService: service,
            kSecAttrAccount: origin, kSecReturnData: true, kSecMatchLimit: kSecMatchLimitOne]
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data,
              let value = try? CampWire.decoder().decode(CloudCredential.self, from: data), value.origin == origin else { throw CampCloudError.credentialsUnavailable }
        return value
    }
    func save(_ credential: CloudCredential) throws {
        let query: [CFString: Any] = [kSecClass: kSecClassGenericPassword, kSecAttrService: service, kSecAttrAccount: credential.origin]
        let data = try CampWire.encoder().encode(credential)
        let update: [CFString: Any] = [kSecValueData: data]
        let status = SecItemUpdate(query as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var insert = query; insert[kSecValueData] = data
            insert[kSecAttrAccessible] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            guard SecItemAdd(insert as CFDictionary, nil) == errSecSuccess else { throw CampCloudError.credentialsUnavailable }
        } else if status != errSecSuccess { throw CampCloudError.credentialsUnavailable }
    }
    func remove(origin: String) throws {
        let query: [CFString: Any] = [kSecClass: kSecClassGenericPassword, kSecAttrService: service, kSecAttrAccount: origin]
        let status = SecItemDelete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound { throw CampCloudError.credentialsUnavailable }
    }
}

#if CAMP_TESTING || CAMP_DEVELOPMENT
final class MemoryCampCredentials: CampCredentialStore, @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: CloudCredential] = [:]
    func read(origin: String) throws -> CloudCredential? { lock.lock(); defer { lock.unlock() }; return values[origin] }
    func save(_ credential: CloudCredential) throws { lock.lock(); defer { lock.unlock() }; values[credential.origin] = credential }
    func remove(origin: String) throws { lock.lock(); defer { lock.unlock() }; values[origin] = nil }
}
#endif
