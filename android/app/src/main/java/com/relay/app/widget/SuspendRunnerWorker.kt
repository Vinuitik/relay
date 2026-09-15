package com.relay.app.widget

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.flow.first

/**
 * Runs the "Suspend now" action from RunnerListScreen's overflow menu — POSTs `/v1/suspend` on
 * one runner, the manual counterpart to its own automatic idle-suspend timeout (see
 * runner/FLOWS.md "Idle-suspend"). Not retried on a `409` (a session is busy - the runner itself
 * refuses, correctly, and retrying won't change that until the user tries again) or `503` (manual
 * suspend isn't enabled on that runner) - those are terminal outcomes, only logged. A network-ish
 * failure (runner briefly unreachable) does get one retry, same as the other workers here.
 */
class SuspendRunnerWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val hostname = inputData.getString(KEY_TARGET_HOSTNAME)
            ?: run {
                Log.w(TAG, "No target runner hostname passed")
                return Result.failure()
            }

        val target = KnownRunnersRepository(applicationContext).runners.first()
            .find { it.hostname == hostname }
            ?: run {
                Log.w(TAG, "'$hostname' is not a known runner")
                return Result.failure()
            }

        return try {
            val response = RelayApiClient.forRunner(target).suspend()
            response.body()?.close()
            when {
                response.isSuccessful -> Result.success()
                response.code() == 409 -> {
                    Log.w(TAG, "Suspend refused for '$hostname': a session is busy")
                    Result.failure()
                }
                response.code() == 503 -> {
                    Log.w(TAG, "Suspend not enabled on '$hostname' (RELAY_IDLE_SUSPEND_ENABLED)")
                    Result.failure()
                }
                else -> {
                    Log.w(TAG, "Suspend failed for '$hostname': HTTP ${response.code()}")
                    Result.retry()
                }
            }
        } catch (e: Exception) {
            Log.e(TAG, "Suspend failed for '$hostname'", e)
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "SuspendRunnerWorker"
        const val KEY_TARGET_HOSTNAME = "target_hostname"
    }
}
