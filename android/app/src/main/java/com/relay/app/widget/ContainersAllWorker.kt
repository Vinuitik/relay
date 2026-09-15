package com.relay.app.widget

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.flow.first

/**
 * Runs the "start all" / "stop all" containers action for one runner (RunnerListScreen row):
 * POSTs `/v1/containers/start-all` or `/v1/containers/stop-all`, which runs docker compose across
 * every project that runner knows about — lets you spin a runner's whole sandbox up or down in
 * one tap instead of per-project, per shared/API.md's bulk endpoints.
 *
 * WorkManager (not a raw coroutine) for the same reason as [StopContainersWorker]/
 * [WakeRunnerWorker] — guarantees the request gets a chance to run and can retry on failure, even
 * if the tap happens while the app process isn't otherwise fully alive.
 */
class ContainersAllWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val hostname = inputData.getString(KEY_TARGET_HOSTNAME)
            ?: run {
                Log.w(TAG, "No target runner hostname passed")
                return Result.failure()
            }
        val start = inputData.getBoolean(KEY_START, true)

        val target = KnownRunnersRepository(applicationContext).runners.first()
            .find { it.hostname == hostname }
            ?: run {
                Log.w(TAG, "'$hostname' is not a known runner")
                return Result.failure()
            }

        return try {
            val api = RelayApiClient.forRunner(target)
            val results = if (start) api.startAllContainers() else api.stopAllContainers()
            val failed = results.filterNot { it.ok }
            if (failed.isNotEmpty()) {
                Log.w(TAG, "${failed.size}/${results.size} projects failed on '$hostname': $failed")
            }
            Result.success()
        } catch (e: Exception) {
            Log.e(TAG, "Containers ${if (start) "start-all" else "stop-all"} failed for '$hostname'", e)
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "ContainersAllWorker"
        const val KEY_TARGET_HOSTNAME = "target_hostname"
        const val KEY_START = "start"
    }
}
