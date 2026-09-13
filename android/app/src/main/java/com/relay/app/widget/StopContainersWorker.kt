package com.relay.app.widget

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.relay.app.data.WidgetConfigRepository
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.flow.first

/**
 * Runs the widget's "Stop" button action: POSTs `/v1/projects/{projectId}/containers/stop` for
 * whatever project is currently configured as the widget default (see
 * [com.relay.app.data.WidgetConfigRepository], set from ProjectListScreen). WorkManager rather
 * than a raw coroutine because widget button taps can fire while the app process isn't otherwise
 * alive — WorkManager guarantees the request gets a chance to run and can retry on failure.
 */
class StopContainersWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val target = WidgetConfigRepository(applicationContext).defaultTarget.first()
            ?: run {
                Log.w(TAG, "No widget default project configured — nothing to stop")
                return Result.failure()
            }

        return try {
            val api = RelayApiClient.forRunner(target.runner)
            val response = api.stopContainers(target.projectId)
            response.body()?.close()
            if (response.isSuccessful) {
                Result.success()
            } else {
                Log.w(TAG, "Stop containers failed: HTTP ${response.code()}")
                Result.retry()
            }
        } catch (e: Exception) {
            Log.e(TAG, "Stop containers failed", e)
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "StopContainersWorker"
    }
}
