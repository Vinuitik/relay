package com.relay.app.data

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.relay.app.data.db.RelayDatabase
import com.relay.app.data.db.UptimeIntervalEntity
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.flow.first

/**
 * Pulls `GET /v1/uptime` from every known runner that happens to be reachable right now, and
 * merges the intervals into the app's own long-term Room table (see
 * [com.relay.app.data.db.UptimeIntervalEntity]'s doc comment for why the phone, not the runner,
 * is where the dashboard's weekly history actually lives). An unreachable runner (asleep, or on a
 * network the phone can't currently reach) is skipped for this run - it'll be caught on the next
 * periodic run, and whatever it hasn't synced yet is exactly what runner/FLOWS.md's "Uptime
 * tracking" short-term buffer exists to hold onto meanwhile.
 *
 * Scheduled periodically (see [com.relay.app.MainActivity], WorkManager's minimum periodic
 * interval is 15 minutes) rather than run continuously - a dashboard showing "as of last sync"
 * data is an accepted tradeoff for a personal, low-traffic app, not a design gap to close.
 */
class UptimeSyncWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val runners = KnownRunnersRepository(applicationContext).runners.first()
        if (runners.isEmpty()) return Result.success()

        val dao = RelayDatabase.get(applicationContext).uptimeDao()
        var anyFailed = false

        for (runner in runners) {
            try {
                val intervals = RelayApiClient.forRunner(runner).uptime()
                dao.upsertAll(
                    intervals.map { UptimeIntervalEntity(runnerHostname = runner.hostname, start = it.start, end = it.end) },
                )
            } catch (e: Exception) {
                anyFailed = true
                Log.d(TAG, "Could not sync uptime from ${runner.hostname} (likely asleep/unreachable): ${e.message}")
            }
        }

        // Retry only if EVERY runner failed - one asleep runner among several reachable ones is
        // an expected, not a worker-level, failure (see class doc: "skipped, caught next time").
        return if (anyFailed && runners.size == 1) Result.retry() else Result.success()
    }

    companion object {
        private const val TAG = "UptimeSyncWorker"
    }
}
