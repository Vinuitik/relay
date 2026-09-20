package com.relay.app.fcm

import android.content.Context
import android.util.Log
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.network.DeviceRegistrationRequest
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.flow.first

/**
 * POSTs `/v1/devices` (this phone's current FCM token) to EVERY known runner, so each one can
 * push "session finished" notifications to this device (see ARCHITECTURE.md "Notifications:
 * FCM"). Runs on token refresh ([RelayFirebaseMessagingService.onNewToken]) and once at app
 * startup with the current token (so a runner added after the token was already issued still
 * gets registered without waiting for a refresh — see MainActivity).
 *
 * WorkManager because this can be triggered from a background FCM callback, not just while the
 * UI is in the foreground.
 *
 * A single unreachable runner logs and is skipped — it must not block registering the rest.
 */
class RegisterDeviceWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val token = inputData.getString(KEY_FCM_TOKEN)
        if (token.isNullOrBlank()) {
            Log.w(TAG, "No FCM token supplied — nothing to register")
            return Result.failure()
        }

        val runners = KnownRunnersRepository(applicationContext).runners.first()
        if (runners.isEmpty()) {
            Log.d(TAG, "No known runners yet — nothing to register with")
            return Result.success()
        }

        var anyFailed = false
        for (runner in runners) {
            try {
                RelayApiClient.forRunner(runner).registerDevice(DeviceRegistrationRequest(token))
            } catch (e: Exception) {
                Log.w(TAG, "Failed to register device with '${runner.hostname}' — skipping it", e)
                anyFailed = true
            }
        }
        return if (anyFailed) Result.retry() else Result.success()
    }

    companion object {
        private const val TAG = "RegisterDeviceWorker"
        const val KEY_FCM_TOKEN = "fcm_token"
    }
}
