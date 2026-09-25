// file: tests/audiobooth-decode/Sources/AudioBoothModels/AppShims.swift
// version: 1.0.0
// guid: 8c1f4e27-5a93-4b60-9d7e-0b2a6f3c8e15
// last-edited: 2026-09-25

import Foundation

// Stand-ins for the two app-level types AudioBooth's model files reference outside the
// decode path. The upstream definitions cannot be staged: Models/Server.swift is
// @Observable app state backed by Keychain storage, and Audiobookshelf.swift imports
// Nuke. Neither shim takes part in decoding; they exist so the model files compile
// UNMODIFIED.

// `Connection.init(_ server: Server)` is a convenience initializer that copies these
// properties. Connection.swift also declares `Credentials` and `JWT`, which `User` uses,
// so the file has to compile.
@MainActor
public final class Server {
  public let id: String = ""
  public let baseURL = URL(fileURLWithPath: "/")
  public let token = Credentials.legacy(token: "")
  public let customHeaders: [String: String] = [:]
  public let alias: String? = nil
  public let alternativeURL: URL? = nil
  public let isUsingAlternativeURL = false
}

// Read only by @MainActor image/cover URL helpers (Author.imageURL, Book.coverURL, ...),
// never by init(from:).
@MainActor
public final class Audiobookshelf {
  public static let shared = Audiobookshelf()
  public var serverURL: URL? { nil }
}
