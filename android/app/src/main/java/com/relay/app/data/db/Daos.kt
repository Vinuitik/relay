package com.relay.app.data.db

import androidx.room.Dao
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.Transaction
import kotlinx.coroutines.flow.Flow

@Dao
interface SessionCacheDao {

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun upsertSession(session: CachedSessionEntity)

    @Query("DELETE FROM cached_messages WHERE runnerHostname = :runnerHostname AND sessionId = :sessionId")
    suspend fun clearMessages(runnerHostname: String, sessionId: String)

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun insertMessages(messages: List<CachedMessageEntity>)

    /**
     * Write-through cache point: called on every successful session poll (see ChatScreen /
     * SessionListScreen) to mirror the runner's full, authoritative state locally. Messages are
     * cleared and reinserted wholesale rather than diffed - the runner always returns the
     * complete transcript, there's no incremental delta to reconcile.
     */
    @Transaction
    suspend fun replaceSession(session: CachedSessionEntity, messages: List<CachedMessageEntity>) {
        upsertSession(session)
        clearMessages(session.runnerHostname, session.sessionId)
        insertMessages(messages)
    }

    @Query("SELECT * FROM cached_sessions WHERE runnerHostname = :runnerHostname AND projectId = :projectId ORDER BY createdAt DESC")
    fun observeSessionsForProject(runnerHostname: String, projectId: String): Flow<List<CachedSessionEntity>>

    @Query("SELECT * FROM cached_sessions WHERE runnerHostname = :runnerHostname AND sessionId = :sessionId LIMIT 1")
    fun observeSession(runnerHostname: String, sessionId: String): Flow<CachedSessionEntity?>

    @Query("SELECT * FROM cached_messages WHERE runnerHostname = :runnerHostname AND sessionId = :sessionId ORDER BY at ASC")
    fun observeMessages(runnerHostname: String, sessionId: String): Flow<List<CachedMessageEntity>>
}

@Dao
interface UptimeDao {

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun upsertAll(intervals: List<UptimeIntervalEntity>)

    @Query("SELECT * FROM uptime_intervals WHERE start >= :sinceIso ORDER BY start ASC")
    fun observeSince(sinceIso: String): Flow<List<UptimeIntervalEntity>>

    @Query("SELECT * FROM uptime_intervals ORDER BY start ASC")
    fun observeAll(): Flow<List<UptimeIntervalEntity>>
}
