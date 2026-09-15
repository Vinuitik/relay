package com.relay.app.data.db

import android.content.Context
import androidx.room.Database
import androidx.room.Room
import androidx.room.RoomDatabase

/**
 * The app's on-device cache — a mirror of runner state (sessions/messages, so chats stay readable
 * offline or while a runner sleeps) plus the phone's own long-term uptime history (see
 * [UptimeIntervalEntity]'s doc comment for why that table, not the runner, is the source of
 * truth). Strictly a read cache for the first two tables and an accumulating log for uptime -
 * never the other way around, so there's no local-write-conflicts-with-runner case to handle.
 */
@Database(
    entities = [CachedSessionEntity::class, CachedMessageEntity::class, UptimeIntervalEntity::class],
    version = 1,
)
abstract class RelayDatabase : RoomDatabase() {
    abstract fun sessionCacheDao(): SessionCacheDao
    abstract fun uptimeDao(): UptimeDao

    companion object {
        @Volatile
        private var instance: RelayDatabase? = null

        fun get(context: Context): RelayDatabase =
            instance ?: synchronized(this) {
                instance ?: Room.databaseBuilder(
                    context.applicationContext,
                    RelayDatabase::class.java,
                    "relay.db",
                ).build().also { instance = it }
            }
    }
}
