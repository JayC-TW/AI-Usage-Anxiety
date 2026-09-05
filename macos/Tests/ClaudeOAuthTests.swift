import Foundation

@main struct ClaudeOAuthTests {
    static func main() {
        let expiredJSON = #"{"claudeAiOauth":{"accessToken":"access-token-fixture","refreshToken":"refresh-token-fixture","expiresAt":1788536224617}}"#
        let expired = parseClaudeOAuthCredential(Data(expiredJSON.utf8))!
        let now = Date(timeIntervalSince1970: 1788536225)
        precondition(expired.accessToken == "access-token-fixture")
        precondition(expired.refreshToken == "refresh-token-fixture")
        precondition(claudeCredentialIsExpired(expired, now: now))

        let validJSON = #"{"claudeAiOauth":{"accessToken":"access-token-fixture","expiresAt":1788560000000}}"#
        let valid = parseClaudeOAuthCredential(Data(validJSON.utf8))!
        precondition(!claudeCredentialIsExpired(valid, now: now))

        let topLevel = #"{"accessToken":"top-level-token"}"#
        precondition(parseClaudeOAuthCredential(Data(topLevel.utf8))?.accessToken == "top-level-token")
        precondition(parseClaudeOAuthCredential(Data(#"{"claudeAiOauth":{}}"#.utf8)) == nil)
        print("PASS: Claude OAuth credential parsing and expiry selection")
    }
}
