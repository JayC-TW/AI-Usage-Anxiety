import Foundation

@main struct KeychainTests {
    static func main() throws {
        let store = KeychainService(service: "local.aiusage.test." + UUID().uuidString, account: "fixture")
        defer { try? store.delete() }
        let before = try store.load()
        precondition(before == nil)
        try store.save("fake-key-one")
        let first = try store.load()
        precondition(first == "fake-key-one")
        try store.save("fake-key-two")
        let second = try store.load()
        precondition(second == "fake-key-two")
        try store.delete()
        let after = try store.load()
        precondition(after == nil)
        try store.delete()
        do { try store.save(""); fatalError("empty accepted") } catch {}
        print("PASS: isolated Keychain create/read/update/delete/missing/empty")
    }
}
