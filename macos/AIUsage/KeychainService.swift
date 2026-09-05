import Foundation
import Security
import LocalAuthentication

protocol KeychainAccess: Sendable {
    func load(interactive: Bool) throws -> String?
    func save(_ key: String) throws
    func delete() throws
}

protocol ClaudeOAuthTokenAccess: Sendable {
    func loadClaudeOAuthToken(interactive: Bool) throws -> String?
}

protocol ClaudeOAuthRefreshAccess: Sendable {
    func loadClaudeOAuthTokenWithRefresh(interactive: Bool) async throws -> String?
}

struct KeychainService: KeychainAccess, ClaudeOAuthTokenAccess, ClaudeOAuthRefreshAccess {
    var service = "aiusage.opencode-go"
    var account = "aiusage"
    private var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
    }
    func load(interactive: Bool = false) throws -> String? {
        var request = query
        request[kSecReturnData as String] = true
        request[kSecMatchLimit as String] = kSecMatchLimitOne
        if !interactive {
            let context = LAContext()
            context.interactionNotAllowed = true
            request[kSecUseAuthenticationContext as String] = context
            request[kSecUseAuthenticationUI as String] = kSecUseAuthenticationUISkip
        }
        var result: CFTypeRef?
        let status = SecItemCopyMatching(request as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess else { throw KeychainFailure(status: status) }
        guard let data = result as? Data, let text = String(data: data, encoding: .utf8), !text.isEmpty else { return nil }
        return text
    }
    func save(_ key: String) throws {
        guard !key.isEmpty, !key.contains("\n"), !key.contains("\r"), key.utf8.count <= 8192 else { throw KeychainFailure(status: errSecParam) }
        let attributes = [kSecValueData as String: Data(key.utf8)]
        var status = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if status == errSecItemNotFound {
            var item = query
            item[kSecValueData as String] = Data(key.utf8)
            status = SecItemAdd(item as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw KeychainFailure(status: status) }
    }
    func delete() throws {
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainFailure(status: status) }
    }

    func loadClaudeOAuthToken(interactive: Bool = false) throws -> String? {
        let records = try claudeCredentialRecords(interactive: interactive)
        return bestClaudeCredential(records, now: Date(), usableOnly: false)?.credential.accessToken
    }

    func loadClaudeOAuthTokenWithRefresh(interactive: Bool = false) async throws -> String? {
        let records = try claudeCredentialRecords(interactive: interactive)
        let now = Date()
        if let current = bestClaudeCredential(records, now: now, usableOnly: true) {
            return current.credential.accessToken
        }

        guard let expired = records
            .filter({ $0.credential.refreshToken?.isEmpty == false && claudeCredentialIsExpired($0.credential, now: now) })
            .sorted(by: betterClaudeCredential)
            .first else { return nil }

        do {
            let refreshed = try await refreshClaudeCredential(expired, interactive: interactive)
            return refreshed.accessToken
        } catch {
            // An expired OAuth credential must not make the whole dashboard fail.
            // The helper will use Claude's local transcript fallback instead.
            return nil
        }
    }
}

struct ClaudeOAuthCredential: Sendable {
    let accessToken: String
    let refreshToken: String?
    let expiresAt: Date?
    let clientID: String?
}

private struct ClaudeCredentialEnvelope: Decodable {
    let claudeAiOauth: ClaudeOAuthPayload?
    let accessToken: String?
}

private struct ClaudeOAuthPayload: Decodable {
    let accessToken: String?
    let refreshToken: String?
    let expiresAt: Double?
    let clientID: String?

    enum CodingKeys: String, CodingKey {
        case accessToken
        case refreshToken
        case expiresAt
        case clientID = "clientId"
    }
}

private struct ClaudeCredentialRecord {
    let service: String
    let account: String?
    let date: Date
    let data: Data
    let credential: ClaudeOAuthCredential
}

private struct ClaudeOAuthRefreshResponse: Decodable {
    let accessToken: String?
    let refreshToken: String?
    let expiresIn: Double?

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
        case expiresIn = "expires_in"
    }
}

private let claudeCredentialService = "Claude Code-credentials"
private let claudeOAuthRefreshEndpoint = "https://platform.claude.com/v1/oauth/token"
private let claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
private let claudeOAuthExpirySkew: TimeInterval = 60

func parseClaudeOAuthCredential(_ data: Data) -> ClaudeOAuthCredential? {
    guard let envelope = try? JSONDecoder().decode(ClaudeCredentialEnvelope.self, from: data) else { return nil }
    let payload = envelope.claudeAiOauth
    guard let token = validClaudeOAuthToken(payload?.accessToken ?? envelope.accessToken) else { return nil }
    return ClaudeOAuthCredential(
        accessToken: token,
        refreshToken: validClaudeOAuthToken(payload?.refreshToken),
        expiresAt: payload?.expiresAt.flatMap(claudeOAuthDate),
        clientID: payload?.clientID
    )
}

func claudeCredentialIsExpired(_ credential: ClaudeOAuthCredential, now: Date) -> Bool {
    guard let expiresAt = credential.expiresAt else { return false }
    return expiresAt <= now.addingTimeInterval(claudeOAuthExpirySkew)
}

private func validClaudeOAuthToken(_ value: String?) -> String? {
    guard let token = value?.trimmingCharacters(in: .whitespacesAndNewlines),
          !token.isEmpty, !token.contains("\n"), !token.contains("\r"), token.utf8.count <= 8192 else { return nil }
    return token
}

private func claudeOAuthDate(_ value: Double) -> Date? {
    guard value.isFinite, value >= 0 else { return nil }
    let seconds = value > 100_000_000_000 ? value / 1000 : value
    guard seconds.isFinite else { return nil }
    return Date(timeIntervalSince1970: seconds)
}

private func claudeCredentialRecords(interactive: Bool) throws -> [ClaudeCredentialRecord] {
    // Query only Claude Code's known service. Enumerating all generic-password
    // items can trigger an unrelated Keychain access prompt in the background.
    guard let data = try claudeData(service: claudeCredentialService, account: nil, interactive: interactive),
          let credential = parseClaudeOAuthCredential(data) else { return [] }
    return [ClaudeCredentialRecord(
        service: claudeCredentialService,
        account: nil,
        date: .distantPast,
        data: data,
        credential: credential
    )]
}

private func bestClaudeCredential(_ records: [ClaudeCredentialRecord], now: Date, usableOnly: Bool) -> ClaudeCredentialRecord? {
    records
        .filter { !usableOnly || !claudeCredentialIsExpired($0.credential, now: now) }
        .sorted(by: betterClaudeCredential)
        .first
}

private func betterClaudeCredential(_ lhs: ClaudeCredentialRecord, _ rhs: ClaudeCredentialRecord) -> Bool {
    switch (lhs.credential.expiresAt, rhs.credential.expiresAt) {
    case let (left?, right?) where left != right:
        return left > right
    case (.some, .none):
        return true
    case (.none, .some):
        return false
    default:
        return lhs.date > rhs.date
    }
}

private func refreshClaudeCredential(_ record: ClaudeCredentialRecord, interactive: Bool) async throws -> ClaudeOAuthCredential {
    guard let refreshToken = record.credential.refreshToken else { throw KeychainFailure(status: errSecParam) }
    guard let url = URL(string: claudeOAuthRefreshEndpoint) else { throw KeychainFailure(status: errSecParam) }
    var request = URLRequest(url: url)
    request.httpMethod = "POST"
    request.timeoutInterval = 5
    request.setValue("application/json", forHTTPHeaderField: "Accept")
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    let body: [String: String] = [
        "grant_type": "refresh_token",
        "refresh_token": refreshToken,
        "client_id": record.credential.clientID ?? claudeOAuthClientID
    ]
    request.httpBody = try JSONSerialization.data(withJSONObject: body)

    let (data, response) = try await URLSession.shared.data(for: request)
    guard let httpResponse = response as? HTTPURLResponse,
          (200..<300).contains(httpResponse.statusCode) else {
        throw KeychainFailure(status: errSecAuthFailed)
    }
    let refreshed = try JSONDecoder().decode(ClaudeOAuthRefreshResponse.self, from: data)
    guard let accessToken = validClaudeOAuthToken(refreshed.accessToken),
          let expiresIn = refreshed.expiresIn,
          expiresIn.isFinite, expiresIn > 0 else {
        throw KeychainFailure(status: errSecDecode)
    }
    let credential = ClaudeOAuthCredential(
        accessToken: accessToken,
        refreshToken: validClaudeOAuthToken(refreshed.refreshToken) ?? record.credential.refreshToken,
        expiresAt: Date().addingTimeInterval(expiresIn),
        clientID: record.credential.clientID ?? claudeOAuthClientID
    )
    try persistClaudeCredential(credential, original: record, interactive: interactive)
    return credential
}

private func persistClaudeCredential(_ credential: ClaudeOAuthCredential, original: ClaudeCredentialRecord, interactive: Bool) throws {
    guard var root = try JSONSerialization.jsonObject(with: original.data) as? [String: Any] else {
        throw KeychainFailure(status: errSecDecode)
    }
    var oauth = root["claudeAiOauth"] as? [String: Any] ?? [:]
    oauth["accessToken"] = credential.accessToken
    if let refreshToken = credential.refreshToken { oauth["refreshToken"] = refreshToken }
    if let expiresAt = credential.expiresAt {
        oauth["expiresAt"] = NSNumber(value: expiresAt.timeIntervalSince1970 * 1000)
    }
    root["claudeAiOauth"] = oauth
    let data = try JSONSerialization.data(withJSONObject: root)
    try updateClaudeData(data, service: original.service, account: original.account, interactive: interactive)
}

private func claudeOAuthToken(from data: Data) -> String? {
    parseClaudeOAuthCredential(data)?.accessToken
}

private func claudeData(service: String, account: String?, interactive: Bool) throws -> Data? {
    var request: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: service,
        kSecReturnData as String: true,
        kSecMatchLimit as String: kSecMatchLimitOne
    ]
    if let account { request[kSecAttrAccount as String] = account }
    if !interactive {
        let context = LAContext()
        context.interactionNotAllowed = true
        request[kSecUseAuthenticationContext as String] = context
        request[kSecUseAuthenticationUI as String] = kSecUseAuthenticationUISkip
    }
    var result: CFTypeRef?
    let status = SecItemCopyMatching(request as CFDictionary, &result)
    if status == errSecItemNotFound { return nil }
    guard status == errSecSuccess else { throw KeychainFailure(status: status) }
    return result as? Data
}

private func updateClaudeData(_ data: Data, service: String, account: String?, interactive: Bool) throws {
    var request: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: service
    ]
    if let account { request[kSecAttrAccount as String] = account }
    if !interactive {
        let context = LAContext()
        context.interactionNotAllowed = true
        request[kSecUseAuthenticationContext as String] = context
        request[kSecUseAuthenticationUI as String] = kSecUseAuthenticationUISkip
    }
    let attributes = [kSecValueData as String: data]
    let status = SecItemUpdate(request as CFDictionary, attributes as CFDictionary)
    guard status == errSecSuccess else { throw KeychainFailure(status: status) }
}

struct KeychainFailure: Error, LocalizedError {
    let status: OSStatus
    var errorDescription: String? { "Keychain 操作失敗（\(status)），請確認存取權限後重試。" }
}
