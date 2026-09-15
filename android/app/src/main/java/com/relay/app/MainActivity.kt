package com.relay.app

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.core.content.ContextCompat
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.workDataOf
import com.google.firebase.appdistribution.AppDistributionRelease
import com.google.firebase.appdistribution.FirebaseAppDistribution
import com.google.firebase.messaging.FirebaseMessaging
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.UptimeSyncWorker
import com.relay.app.data.WidgetConfigRepository
import com.relay.app.fcm.RegisterDeviceWorker
import java.util.concurrent.TimeUnit
import com.relay.app.ui.navigation.RelayNavHost
import com.relay.app.ui.theme.RelayTheme

class MainActivity : ComponentActivity() {

    // Not `remember`-ed — this is a plain property on the Activity itself, so it survives
    // recomposition the normal Compose-state way (same MutableState instance every time
    // setContent's lambda re-runs) without needing a ViewModel for one dialog's worth of state.
    private var pendingUpdate = mutableStateOf<AppDistributionRelease?>(null)

    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (!granted) {
                Log.w(TAG, "Notification permission denied — job-done pushes won't show a banner")
            }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val runnersRepository = KnownRunnersRepository(applicationContext)
        val widgetConfigRepository = WidgetConfigRepository(applicationContext)

        registerCurrentFcmTokenWithAllRunners()
        requestNotificationPermissionIfNeeded()
        checkForUpdate()
        scheduleUptimeSync()

        setContent {
            RelayTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    RelayNavHost(
                        runnersRepository = runnersRepository,
                        widgetConfigRepository = widgetConfigRepository,
                    )
                }
                val release by pendingUpdate
                if (release != null) {
                    UpdateAvailableDialog(
                        release = release!!,
                        onDismiss = { pendingUpdate.value = null },
                        onUpdate = {
                            pendingUpdate.value = null
                            FirebaseAppDistribution.getInstance().updateApp()
                        },
                    )
                }
            }
        }
    }

    /**
     * Checks Firebase App Distribution for a newer release than what's installed - the
     * "Notion-style" in-app update prompt. First call ever on a device triggers the SDK's own
     * tester sign-in (a Custom Tab, one-time); after that this is silent until there's actually
     * something new. Same try/catch-on-unconfigured-Firebase pattern as
     * [registerCurrentFcmTokenWithAllRunners] - a checkout without google-services.json must
     * still start normally.
     */
    private fun checkForUpdate() {
        try {
            FirebaseAppDistribution.getInstance().checkForNewRelease()
                .addOnSuccessListener { release -> if (release != null) pendingUpdate.value = release }
                .addOnFailureListener { e -> Log.w(TAG, "Update check failed", e) }
        } catch (e: IllegalStateException) {
            Log.w(TAG, "Firebase not configured yet (no google-services.json) — skipping update check", e)
        }
    }

    /**
     * Registers whatever FCM token currently exists with every known runner (see
     * `POST /v1/devices` in shared/API.md and [RegisterDeviceWorker]) — so a runner added AFTER
     * the token was already issued still gets registered without waiting for
     * [com.relay.app.fcm.RelayFirebaseMessagingService.onNewToken] to fire on refresh.
     *
     * A Firebase project now exists and `google-services.json` is present (see
     * app/build.gradle.kts — the `com.google.gms.google-services` plugin applies automatically
     * when that file is there). The try/catch below stays as a safety net: a checkout without
     * that gitignored file (e.g. a fresh clone before it's regenerated) would otherwise crash
     * app startup instead of just skipping registration.
     */
    private fun registerCurrentFcmTokenWithAllRunners() {
        try {
            FirebaseMessaging.getInstance().token.addOnCompleteListener { task ->
                val token = task.result
                if (task.isSuccessful && !token.isNullOrBlank()) {
                    val request = OneTimeWorkRequestBuilder<RegisterDeviceWorker>()
                        .setInputData(workDataOf(RegisterDeviceWorker.KEY_FCM_TOKEN to token))
                        .build()
                    WorkManager.getInstance(applicationContext).enqueue(request)
                } else {
                    Log.w(TAG, "Could not fetch FCM token at startup", task.exception)
                }
            }
        } catch (e: IllegalStateException) {
            Log.w(TAG, "Firebase not configured yet (no google-services.json) — skipping startup FCM registration", e)
        }
    }

    /**
     * Schedules [UptimeSyncWorker] every 15 minutes (WorkManager's minimum periodic interval) -
     * pulls `GET /v1/uptime` from whichever known runners happen to be reachable right now and
     * merges it into the app's own long-term Room history (see DashboardScreen). Unique work so
     * repeated `onCreate` calls (rotation, process restart) don't stack up duplicate schedules.
     */
    private fun scheduleUptimeSync() {
        val request = PeriodicWorkRequestBuilder<UptimeSyncWorker>(15, TimeUnit.MINUTES).build()
        WorkManager.getInstance(applicationContext)
            .enqueueUniquePeriodicWork("uptime-sync", ExistingPeriodicWorkPolicy.KEEP, request)
    }

    /**
     * Android 13+ (API 33) requires explicit runtime consent to show any notification,
     * including the job-done push in [com.relay.app.fcm.RelayFirebaseMessagingService]. Below
     * API 33 this permission doesn't exist and notifications just work.
     */
    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val alreadyGranted = ContextCompat.checkSelfPermission(
            this,
            Manifest.permission.POST_NOTIFICATIONS,
        ) == PackageManager.PERMISSION_GRANTED
        if (!alreadyGranted) {
            requestNotificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    companion object {
        private const val TAG = "MainActivity"
    }
}

@Composable
private fun UpdateAvailableDialog(
    release: AppDistributionRelease,
    onDismiss: () -> Unit,
    onUpdate: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Update available") },
        text = { Text("Relay ${release.displayVersion} is ready to install.") },
        confirmButton = { TextButton(onClick = onUpdate) { Text("Update") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Later") } },
    )
}
