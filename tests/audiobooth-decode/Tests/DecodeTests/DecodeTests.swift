// file: tests/audiobooth-decode/Tests/DecodeTests/DecodeTests.swift
// version: 1.1.0
// guid: 1e5c8f36-2a9d-4b71-a4c0-9f3d6b2e7a85
// last-edited: 2026-09-25

import Foundation
import XCTest

@testable import AudioBoothModels

// Decodes every fixture our ABS handlers produced (internal/server/handlers/abs/
// audiobooth_fixtures_test.go) through AudioBooth's own models, using the decode type
// each app call site declares (manifest.json). Swift decoding is all-or-nothing: one
// missing required field blanks the whole screen, which is exactly how #3438 shipped
// a blank search while every endpoint returned 200.

private let harnessDir = URL(fileURLWithPath: #filePath)
  .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

private struct Row {
  let id: String
  let callSite: String
  let decode: String
  let extra: Bool
  let na: String?
  let vacuous: String?
  let expectStatus: Int?
}

private struct IndexEntry: Decodable {
  let status: Int
  let contentType: String
  let file: String
}

private func loadManifest() throws -> (callSites: [String], rows: [Row]) {
  let data = try Data(contentsOf: harnessDir.appendingPathComponent("manifest.json"))
  let json = try JSONSerialization.jsonObject(with: data) as! [String: Any]
  let rows = (json["requests"] as! [[String: Any]]).map {
    Row(
      id: $0["id"] as! String,
      callSite: $0["callSite"] as! String,
      decode: $0["decode"] as? String ?? "",
      extra: $0["extra"] as? Bool ?? false,
      na: $0["na"] as? String,
      vacuous: $0["vacuous"] as? String,
      expectStatus: $0["expectStatus"] as? Int
    )
  }
  return (json["callSites"] as! [String], rows)
}

private func loadIndex() throws -> [String: IndexEntry] {
  let url = harnessDir.appendingPathComponent("fixtures/index.json")
  return try JSONDecoder().decode([String: IndexEntry].self, from: Data(contentsOf: url))
}

// MARK: - what a decode touched

// Tally of model elements actually decoded (not merely declared), for the coverage
// report. A model decoded only as an empty array is NOT counted.
private final class Tally {
  var models: [String: Int] = [:]
  var books: [Book] = []

  func add(_ model: String, _ n: Int = 1) {
    if n > 0 { models[model, default: 0] += n }
  }

  func book(_ b: Book) {
    add("Book")
    books.append(b)
    add("AudioTrack", b.media.tracks?.count ?? 0)
  }

  func user(_ u: User) {
    add("User")
    if u.type == .unknown {
      XCTFail("user.type decoded as .unknown; the app would treat this account as an unknown role")
    }
  }
}

// Decodes one fixture as the call site's type and tallies what it held.
private func decode(_ type: String, status: Int, body: Data, tally: Tally) throws {
  func send<T: Decodable>(_ t: T.Type) throws -> T { try AppDecoder.send(t, status: status, body: body) }

  switch type {
  case "Data":
    _ = try send(Data.self)
  case "ServerStatus":
    _ = try send(ServerStatus.self)
    tally.add("ServerStatus")
  case "Authorize":
    let a = try send(Authorize.self)
    tally.add("Authorize")
    tally.add("ServerSettings")
    tally.add("EreaderDevice", a.ereaderDevices.count)
    tally.user(a.user)
  case "LibrariesResponse":
    tally.add("Library", try send(LibrariesResponse.self).libraries.count)
  case "FilterDataResponse":
    let f = try send(FilterDataResponse.self).filterdata
    if !f.authors.isEmpty || !f.series.isEmpty { tally.add("FilterData") }
  case "[Personalized.Section]":
    let sections = try send([Personalized.Section].self)
    tally.add("Personalized", sections.count)
    for s in sections {
      switch s.entities {
      case .books(let bs): bs.forEach(tally.book)
      case .series(let ss):
        tally.add("Series", ss.count)
        ss.flatMap(\.books).forEach(tally.book)
      case .authors(let as_): tally.add("Author", as_.count)
      case .podcasts, .episodes: break
      case .unknown:
        // Personalized.Section maps an unrecognised `type` to .unknown and the
        // shelf silently disappears from Home. That is a decode "success" the user
        // experiences as missing data, so it fails here.
        XCTFail("personalized section \(s.id) has a type the app does not know; it would be dropped from Home")
      }
    }
  case "Page<Book>":
    try send(Page<Book>.self).results.forEach(tally.book)
  case "Book":
    tally.book(try send(Book.self))
  case "SearchResponse":
    let r = try send(SearchResponse.self)
    tally.add("SearchResponse")
    r.book.map(\.libraryItem).forEach(tally.book)
    tally.add("Series", r.series.count)
    r.series.flatMap(\.books).forEach(tally.book)
    tally.add("Author", r.authors.count)
    tally.add("SearchResponse.Narrator", r.narrators.count)
    tally.add("SearchResponse.Genre", r.genres.count)
    tally.add("SearchResponse.Tag", r.tags.count)
  case "Page<Series>":
    let p = try send(Page<Series>.self)
    tally.add("Series", p.results.count)
    p.results.flatMap(\.books).forEach(tally.book)
  case "Page<Author>":
    tally.add("Author", try send(Page<Author>.self).results.count)
  case "AuthorDetails":
    let d = try send(AuthorDetails.self)
    tally.add("AuthorDetails")
    d.libraryItems.forEach(tally.book)
    d.series.flatMap(\.items).forEach(tally.book)
  case "NarratorsResponse":
    tally.add("Narrator", try send(NarratorsResponse.self).narrators.count)
  case "RecentEpisodesResponse":
    tally.add("RecentEpisode", try send(RecentEpisodesResponse.self).episodes.count)
  case "User.MediaProgress":
    _ = try send(User.MediaProgress.self)
    tally.add("User.MediaProgress")
  case "User.Bookmark":
    _ = try send(User.Bookmark.self)
    tally.add("User.Bookmark")
  case "PlaySession":
    let s = try send(PlaySession.self)
    tally.add("PlaySession")
    tally.add("AudioTrack", s.audioTracks?.count ?? 0)
    if case .book(let b) = s.libraryItem { tally.book(b) }
  case "ListeningHistoryResponse":
    let r = try send(ListeningHistoryResponse.self)
    tally.add("ListeningHistoryResponse")
    tally.add("ListeningHistorySession", r.sessions.count)
  case "ItemListeningSessionsResponse":
    tally.add("SessionSync", try send(ItemListeningSessionsResponse.self).sessions.count)
  case "ListeningStats":
    _ = try send(ListeningStats.self)
    tally.add("ListeningStats")
  case "YearStats":
    _ = try send(YearStats.self)
    tally.add("YearStats")
  case "Collection":
    let c = try send(Collection.self)
    tally.add("Collection")
    c.books.forEach(tally.book)
  case "Page<Collection>":
    let p = try send(Page<Collection>.self)
    tally.add("Collection", p.results.count)
    p.results.flatMap(\.books).forEach(tally.book)
  case "Playlist":
    let p = try send(Playlist.self)
    tally.add("Playlist")
    p.books.forEach(tally.book)
  case "Page<Playlist>":
    let p = try send(Page<Playlist>.self)
    tally.add("Playlist", p.results.count)
    p.results.flatMap(\.books).forEach(tally.book)
  case "EmptyResponse":
    _ = try send(EmptyResponse.self)
  default:
    XCTFail("manifest decode type \(type) has no case in DecodeTests.decode")
  }
}

// MARK: - lenient-decode traps

// Book.Media.Metadata decodes `series` with try?, so a malformed series element does
// not throw: the book decodes and the series silently vanishes from the detail screen.
// Count the series entries in every Book-shaped raw object and require the decoded
// books to carry the same number.
private func rawSeriesCount(_ v: Any) -> Int {
  var n = 0
  if let d = v as? [String: Any] {
    if let media = d["media"] as? [String: Any], let meta = media["metadata"] as? [String: Any] {
      if let arr = meta["series"] as? [Any] {
        n += arr.count
      } else if meta["series"] is [String: Any] {
        n += 1
      }
    }
    for (_, e) in d { n += rawSeriesCount(e) }
  } else if let a = v as? [Any] {
    for e in a { n += rawSeriesCount(e) }
  }
  return n
}

// MARK: - tests

final class DecodeTests: XCTestCase {
  func testEveryAppRequestDecodes() throws {
    let (callSites, rows) = try loadManifest()
    let index = try loadIndex()
    XCTAssertEqual(callSites.count, 45, "the pinned app's call-site inventory is 45 paths")

    let tally = Tally()
    var decodedSites = Set<String>(), dataSites = Set<String>(), designedErrorSites = Set<String>()
    var naSites = Set<String>(), failedSites = Set<String>(), vacuousRows: [String] = []
    var withDataSites = Set<String>()
    var lines: [String] = []

    for row in rows {
      if let na = row.na {
        naSites.insert(row.callSite)
        lines.append("  N/A      \(row.id): \(na)")
        continue
      }
      guard let entry = index[row.id] else {
        XCTFail("\(row.id): no fixture; run `make audiobooth-decode` to regenerate")
        failedSites.insert(row.callSite)
        continue
      }
      let body = try Data(contentsOf: harnessDir.appendingPathComponent("fixtures/\(entry.file)"))

      if let want = row.expectStatus {
        // The app's send() throws httpError for this status and shows its message.
        XCTAssertEqual(entry.status, want, "\(row.id): status")
        do {
          try decode(row.decode, status: entry.status, body: body, tally: tally)
          XCTFail("\(row.id): expected send() to throw for HTTP \(want)")
        } catch AppDecoder.SendError.httpError {
          designedErrorSites.insert(row.callSite)
          lines.append("  ERR-OK   \(row.id): HTTP \(entry.status) by design (\(row.vacuous ?? ""))")
        }
        continue
      }

      let before = tally.books.count
      do {
        try decode(row.decode, status: entry.status, body: body, tally: tally)
      } catch {
        XCTFail("\(row.id) (\(row.callSite)) does not decode as \(row.decode): \(error)")
        failedSites.insert(row.callSite)
        lines.append("  FAIL     \(row.id): \(error)")
        continue
      }

      if row.decode != "Data", let raw = try? JSONSerialization.jsonObject(with: body) {
        let decodedSeries = tally.books[before...].reduce(0) { $0 + ($1.media.metadata.series?.count ?? 0) }
        XCTAssertEqual(
          decodedSeries, rawSeriesCount(raw),
          "\(row.id): media.metadata.series entries were dropped by Book's lenient (try?) series decode")
      }

      if row.extra { lines.append("  extra    \(row.id) as \(row.decode)"); continue }
      if row.decode == "Data" {
        dataSites.insert(row.callSite)
        lines.append("  2xx      \(row.id) (Data: the app does not decode)")
      } else {
        decodedSites.insert(row.callSite)
        if row.vacuous == nil { withDataSites.insert(row.callSite) }
        lines.append("  decoded  \(row.id) as \(row.decode)\(row.vacuous == nil ? "" : " (vacuous, see below)")")
      }
      if let v = row.vacuous { vacuousRows.append("\(row.id): \(v)") }
    }

    // A call site counts only if EVERY row for it passed; a Data row does not upgrade
    // a site to "decoded", and N/A is reported separately rather than as a pass. A site
    // whose every decoded row is empty by design is reported apart from the ones that
    // decoded real data: a vacuous pass is exactly what item 6 was closed on before.
    let decoded = decodedSites.subtracting(failedSites)
    let decodedWithData = decoded.intersection(withDataSites)
    let decodedEmpty = decoded.subtracting(withDataSites)
    let dataOnly = dataSites.subtracting(decodedSites).subtracting(failedSites)
    let errOK = designedErrorSites.subtracting(decodedSites).subtracting(dataSites)
    let na = naSites.subtracting(decodedSites).subtracting(dataSites).subtracting(designedErrorSites)

    // The 26 Codable/Decodable structs declared in the pinned app's Models directory.
    let models26: [(String, String?)] = [
      ("AudioTrack", nil), ("Author", nil), ("AuthorDetails", nil), ("Authorize", nil),
      ("Book", nil), ("Collection", nil), ("Connection", "app state (Keychain), never a response"),
      ("EreaderDevice", nil), ("FilterData", nil), ("Library", nil),
      ("ListeningHistoryResponse", nil), ("ListeningHistorySession", nil), ("ListeningStats", nil),
      ("Narrator", nil), ("Personalized", nil), ("Playlist", nil),
      ("Podcast", "podcast libraries only"), ("PodcastEpisode", "podcast libraries only"),
      ("RecentEpisode", "podcast libraries only"), ("SearchResponse", nil), ("Series", nil),
      ("ServerSettings", nil), ("ServerStatus", nil), ("SessionSync", nil), ("User", nil),
      ("YearStats", nil),
    ]
    var modelLines: [String] = []
    var modelsDecoded = 0
    for (name, naReason) in models26 {
      let n = tally.models[name] ?? 0
      if n > 0 {
        modelsDecoded += 1
        // The app never decodes Personalized itself: it decodes [Personalized.Section]
        // and builds the wrapper (LibrariesService.fetchPersonalized).
        let via = name == "Personalized" ? " (as [Personalized.Section] sections)" : ""
        modelLines.append("  decoded  \(name) x\(n)\(via)")
      } else if let r = naReason {
        modelLines.append("  N/A      \(name): \(r)")
      } else {
        modelLines.append("  EMPTY    \(name): only ever decoded as an empty array (vacuous)")
      }
    }

    let report = """
      AudioBooth decode proof (pin: \(pinnedSHA()))
      Call sites: \(decodedWithData.count) of 45 decoded with data, \
      \(decodedEmpty.count) of 45 decoded but empty by design, \
      \(dataOnly.count) of 45 2xx-only (Data), \(errOK.count) of 45 error-by-design, \
      \(na.count) of 45 N/A, \(failedSites.count) of 45 FAILED
      Models: \(modelsDecoded) of 26 decoded with data
      \(lines.joined(separator: "\n"))
      Models:
      \(modelLines.joined(separator: "\n"))
      Vacuous rows (decoded, but proves nothing about populated data):
        \(vacuousRows.joined(separator: "\n  "))
      """
    print(report)
    try? report.write(to: harnessDir.appendingPathComponent("fixtures/REPORT.txt"), atomically: true, encoding: .utf8)
    XCTAssertEqual(decoded.count + dataOnly.count + errOK.count + na.count, 45, "every call site is accounted for")
  }

  // The harness must be able to fail. Each control removes one REQUIRED field from a
  // real fixture and expects the app's decoder to throw; if it does not, a green run
  // proves nothing.
  func testNegativeControls() throws {
    func mutate(_ file: String, _ edit: (inout [String: Any]) -> Void) throws -> Data {
      let data = try Data(contentsOf: harnessDir.appendingPathComponent("fixtures/\(file)"))
      var obj = try JSONSerialization.jsonObject(with: data) as! [String: Any]
      edit(&obj)
      return try JSONSerialization.data(withJSONObject: obj)
    }

    // #3438: search narrators without numBooks blanked every search in the app.
    let noNumBooks = try mutate("search_narrator.json") { obj in
      var ns = obj["narrators"] as! [[String: Any]]
      XCTAssertFalse(ns.isEmpty, "control needs a narrator to strip")
      ns[0].removeValue(forKey: "numBooks")
      obj["narrators"] = ns
    }
    XCTAssertThrowsError(try AppDecoder.send(SearchResponse.self, status: 200, body: noNumBooks))

    // Page<T> requires total and page.
    let noTotal = try mutate("items_title.json") { $0.removeValue(forKey: "total") }
    XCTAssertThrowsError(try AppDecoder.send(Page<Book>.self, status: 200, body: noTotal))

    // Dates are epoch MILLISECONDS as Int64; an ISO string must not decode.
    let isoDate = try mutate("collection_get.json") { $0["createdAt"] = "2026-09-25T00:00:00Z" }
    XCTAssertThrowsError(try AppDecoder.send(Collection.self, status: 200, body: isoDate))

    // Lenient trap: a series element missing its id does NOT throw (Book decodes series
    // with try?); the series silently vanishes. The series-count check in
    // testEveryAppRequestDecodes is what catches that, so prove it would fire.
    let badSeries = try mutate("item_detail.json") { obj in
      var media = obj["media"] as! [String: Any]
      var meta = media["metadata"] as! [String: Any]
      var series = meta["series"] as! [[String: Any]]
      XCTAssertFalse(series.isEmpty, "control needs a series to break")
      series[0].removeValue(forKey: "id")
      meta["series"] = series
      media["metadata"] = meta
      obj["media"] = media
    }
    let book = try AppDecoder.send(Book.self, status: 200, body: badSeries)
    XCTAssertNil(book.media.metadata.series, "upstream series decode is no longer lenient; revisit the series check")
    XCTAssertEqual(rawSeriesCount(try JSONSerialization.jsonObject(with: badSeries)), 1)

    // Any non-2xx throws before decoding, and an empty body throws for a decoded type.
    XCTAssertThrowsError(try AppDecoder.send(Data.self, status: 404, body: Data()))
    XCTAssertThrowsError(try AppDecoder.send(EmptyResponse.self, status: 200, body: Data()))
  }
}

private func pinnedSHA() -> String {
  let pin = (try? String(contentsOf: harnessDir.appendingPathComponent("audiobooth.pin"), encoding: .utf8)) ?? ""
  return pin.split(separator: "\n").first { $0.hasPrefix("sha=") }.map { String($0.dropFirst(4)) } ?? "?"
}
