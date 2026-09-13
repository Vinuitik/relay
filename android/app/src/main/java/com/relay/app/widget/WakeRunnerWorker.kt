package com.relay.app.widget

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.WidgetConfigRepository
import com.relay.app.network.RelayApiClient
import com.relay.app.network.WakeRequest
import kotlinx.coroutines.flow.first

/**
 * Runs a "Wake" button action (RunnerListScreen row, or the widget's Wake button): resolves the
 * target runner's [com.relay.app.model.KnownRunner.wakeMac] and
 * [com.relay.app.model.KnownRunner.wakeViaRunnerId], then POSTs `/v1/wake` to that OTHER runner —
 * per ARCHITECTURE.md "Relay device", a fully-off target can never be called directly, so the
 * magic packet has to come from a different always-on runner on the same LAN.
 *
 * WorkManager (not a raw coroutine) for the same reason as [StopContainersWorker]: a widget tap
 * can fire while the app process isn't otherwise alive.
 *
 * Input data optionally carries [KEY_TARGET_HOSTNAME] — the hostname of the known runner to wake.
 * When absent (the widget's plain Wake button, which has no per-runner UI), this falls back to
 * whichever runner is configured as the widget's default project target.
 */
class WakeRunnerWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val explicitHostname = inputData.getString(KEY_TARGET_HOSTNAME)
        val targetHostname = explicitHostname
            ?: WidgetConfigRepository(applicationContext).defaultTarget.first()?.runner?.hostname
            ?: run {
                Log.w(TAG, "No target runner to wake — no hostname passed and no widget default project configured")
                return Result.failure()
            }

        val runners = KnownRunnersRepository(applicationContext).runners.first()
        val target = runners.find { it.hostname == targetHostname }
            ?: run {
                Log.w(TAG, "Wake target '$targetHostname' is not a known runner")
                return Result.failure()
            }

        val mac = target.wakeMac
        val viaId = target.wakeViaRunnerId
        if (mac.isNullOrBlank() || viaId.isNullOrBlank()) {
            Log.w(TAG, "Wake not configured for '${target.hostname}' — set wakeMac and wakeViaRunnerId first (edit it from RunnerListScreen)")
            return Result.failure()
        }

        val viaRunner = runners.find { it.hostname == viaId }
        if (viaRunner == null) {
            Log.w(TAG, "wakeViaRunnerId '$viaId' for '${target.hostname}' does not match any known runner")
            return Result.failure()
        }

        return try {
            val api = RelayApiClient.forRunner(viaRunner)
            val response = api.wake(WakeRequest(mac))
            response.body()?.close()
            if (response.isSuccessful) {
                Result.success()
            } else {
                Log.w(TAG, "Wake failed: HTTP ${response.code()}")
                Result.retry()
            }
        } catch (e: Exception) {
            Log.e(TAG, "Wake failed", e)
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "WakeRunnerWorker"
        const val KEY_TARGET_HOSTNAME = "target_hostname"
    }
}
