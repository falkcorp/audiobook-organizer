### Fixed

#### `core/outbox` — a cancel between handler return and ack no longer redelivers the message

`settleAck` committed the cursor advance on the relay run's own context. When the
run was canceled after a handler returned nil and before the ack committed
(every graceful shutdown can land there), the commit failed on the canceled
context, the message stayed unacked and the next relay run delivered it a
second time. The ack of an already-delivered message now commits on a context
detached from the run's cancellation and bounded by a 5 second timeout, and a
ack lost at shutdown is logged. This also removes the intermittent failure of
`TestOutbox_RelayRestartsAfterCancel` ("exactly two deliveries did not hold");
`TestOutbox_AckSurvivesCancelBetweenHandlerAndCommit` pins the window
deterministically.
