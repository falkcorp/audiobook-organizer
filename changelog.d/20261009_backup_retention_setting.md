Fixed

- The scheduled backup cleanup (`.bak-*` files) now reads its own `backup_retention_days` setting instead of borrowing the soft-delete retention. The new setting defaults to 0, meaning "same as soft-delete retention", so nothing changes until it is set. The unscheduled maintenance twin now reads the same source. Settings gains a "Backup retention (days)" field.
