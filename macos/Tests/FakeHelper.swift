import Foundation
import Darwin

let mode = URL(fileURLWithPath: CommandLine.arguments[0]).lastPathComponent
_ = FileHandle.standardInput.readDataToEndOfFile()
switch mode {
case "helper-hang":
    signal(SIGTERM, SIG_IGN)
    Thread.sleep(forTimeInterval: 60)
case "helper-large":
    FileHandle.standardOutput.write(Data(repeating: 65, count: 1100000))
default:
    print("not JSON")
}
