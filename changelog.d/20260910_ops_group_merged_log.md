### Changed

#### Operation groups merge when adjacent, and a group row opens one merged log

The bell and the Activity page fold runs of the same operation into one `×N` row, but the fold stopped at a two-minute idle gap and a thirty-minute span, so a long AI Filename Parsing run still showed as a chain of `×N` rows with stray single runs between them. A second pass now merges any same-kind rows that sit next to each other in the timeline, whatever the gap, so the run is one row; a different kind of operation between two runs still keeps them apart. Group rows in the bell were also inert, because a group has no server record of its own. Clicking one now opens a single merged log of every member in time order, each line labelled with the run it came from, live lines included, through a new `POST /api/v1/operations/activity/merged` read that takes the member ids. Nothing is written or persisted; grouping stays a read-time view.
