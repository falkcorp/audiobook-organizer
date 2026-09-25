// swift-tools-version: 6.2
// file: tests/audiobooth-decode/Package.swift
// version: 1.0.0
// guid: 3b7e5a90-c1d4-4f28-8e63-a2f09d5c71b8
// last-edited: 2026-09-25
//
// AudioBooth decode proof. AudioBoothModels compiles AudioBooth's own model sources,
// staged (gitignored) into Sources/AudioBoothModels/Upstream by
// scripts/audiobooth_decode_prep.py from the commit pinned in audiobooth.pin.
// DecodeTests decodes the fixtures our ABS handlers produced
// (internal/server/handlers/abs/audiobooth_fixtures_test.go) through those models,
// using the decode type each app call site declares (manifest.json).

import PackageDescription

let package = Package(
  name: "AudioBoothDecode",
  platforms: [.macOS(.v14)],
  targets: [
    .target(
      name: "AudioBoothModels",
      path: "Sources/AudioBoothModels",
      swiftSettings: [.swiftLanguageMode(.v6)]
    ),
    .testTarget(
      name: "DecodeTests",
      dependencies: ["AudioBoothModels"],
      path: "Tests/DecodeTests"
    ),
  ]
)
