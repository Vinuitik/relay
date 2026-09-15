package com.relay.app.data.db

import androidx.room.Entity
import androidx.room.PrimaryKey

/**
 * On-device mirror of a runner's [com.relay.app.model.Session] (minus its `messages`, cached
 * separately in [CachedMessageEntity]). Keyed by (runnerHostname, sessionId), not sessionId alone
 * - a session id is only unique within one runner, and the whole point of this cache is holding
 * data from every known runner at once.
 */
@Entity(tableName = "cached_sessions", primaryKeys = ["runnerHostname", "sessionId"])
data class CachedSessionEntity(
    val runnerHostname: String,
    val sessionId: String,
    val projectId: String,
    val provider: String,
    val state: String,
    val createdAt: String,
    val finishedAt: String?,
)

/**
 * On-device mirror of one [com.relay.app.model.Message] within a session. Write-through: every
 * successful `GET /v1/sessions/{id}` poll replaces a session's full message set wholesale (see
 * `SessionCacheDao.replaceSession`) rather than diffing - the runner always returns the complete
 * transcript anyway, so there's nothing to reconcile incrementally.
 */
@Entity(tableName = "cached_messages")
data class CachedMessageEntity(
    @PrimaryKey(autoGenerate = true) val id: Long = 0,
    val runnerHostname: String,
    val sessionId: String,
    val role: String,
    val text: String,
    val at: String,
)

/**
 * On-device mirror of one [com.relay.app.model.UptimeInterval], merged in from `GET /v1/uptime`
 * by [com.relay.app.data.UptimeSyncWorker]. This table (not the runner's own short-lived buffer -
 * see runner/FLOWS.md "Uptime tracking") is the dashboard's actual system of record, since a
 * runner that's asleep is exactly the runner you can't ask for its own history.
 *
 * Keyed by (runnerHostname, start): re-syncing the same interval after its `end` was filled in
 * updates the row in place (REPLACE) instead of duplicating it.
 */
@Entity(tableName = "uptime_intervals", primaryKeys = ["runnerHostname", "start"])
data class UptimeIntervalEntity(
    val runnerHostname: String,
    val start: String,
    val end: String?,
)
