// file: tests/audiobooth-decode/Tests/DecodeTests/AppDecoder.swift
// version: 1.0.0
// guid: 7d4a1b93-e6c2-4f05-8b37-5a9e0c2d6f18
// last-edited: 2026-09-25

import Foundation

@testable import AudioBoothModels

// Mirrors NetworkService.send() at the pinned AudioBooth commit
// (API/Sources/API/NetworkService.swift). scripts/audiobooth_decode_prep.py fails the
// run if the upstream lines this depends on change, so a drift here cannot go unseen.
enum AppDecoder {
  // NetworkService.decoder: every Date is an Int64 epoch in MILLISECONDS.
  static let decoder: JSONDecoder = {
    let decoder = JSONDecoder()
    decoder.dateDecodingStrategy = .custom { decoder in
      let container = try decoder.singleValueContainer()
      let timestamp = try container.decode(Int64.self)
      return Date(timeIntervalSince1970: TimeInterval(timestamp / 1000))
    }
    return decoder
  }()

  enum SendError: Error, CustomStringConvertible {
    case httpError(Int, String)
    case emptyBody

    var description: String {
      switch self {
      case .httpError(let code, let body): return "HTTP \(code): \(body.prefix(300))"
      case .emptyBody: return "empty body for a non-Data type (URLError.cannotDecodeContentData)"
      }
    }
  }

  // The status gate and the Data / empty-body branches of performRequest().
  static func send<T: Decodable>(_: T.Type, status: Int, body: Data) throws -> T {
    guard 200...299 ~= status else {
      throw SendError.httpError(status, String(data: body, encoding: .utf8) ?? "")
    }
    if T.self == Data.self {
      return body as! T
    }
    if body.isEmpty {
      throw SendError.emptyBody
    }
    return try decoder.decode(T.self, from: body)
  }
}

// Response wrappers the app declares INSIDE the calling function, so they cannot be
// staged from upstream. Each is copied from the cited function at the pinned commit.

// LibrariesService.fetch(serverID:): struct Response: Codable { let libraries: [Library] }
struct LibrariesResponse: Codable { let libraries: [Library] }

// FilterDataService.performFetch: struct Response: Codable { let filterdata: FilterData }
struct FilterDataResponse: Codable { let filterdata: FilterData }

// NarratorsService.fetch: struct Response: Codable { let narrators: [Narrator] }
struct NarratorsResponse: Codable { let narrators: [Narrator] }

// LibrariesService.fetchRecentEpisodes: struct Response: Decodable { let episodes: [RecentEpisode] }
struct RecentEpisodesResponse: Decodable { let episodes: [RecentEpisode] }

// SessionService.getListeningSessions: struct Response: Codable { numPages, page, itemsPerPage, sessions }
struct ItemListeningSessionsResponse: Codable {
  let numPages: Int
  let page: Int
  let itemsPerPage: Int
  let sessions: [SessionSync]
}

// ProgressService.removeFromContinueListening: struct Response: Codable {}
struct EmptyResponse: Codable {}
