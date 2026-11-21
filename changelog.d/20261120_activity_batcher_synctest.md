### Fixed

- Test only: the activity batcher tests now run inside `synctest` bubbles instead of racing real wall-clock margins (a 50 ms window against a 300 ms guard, a 10 ms "nothing was sent" window, 100 ms drain timeouts). The timer test uses the production 15 s window on the fake clock and also asserts nothing is emitted one tick before it ends; the item-count test submits exactly 500 items and asserts the flush happens with no clock advance.
