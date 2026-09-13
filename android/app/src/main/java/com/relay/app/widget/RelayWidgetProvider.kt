package com.relay.app.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.Context
import android.content.Intent
import android.util.Log
import android.widget.RemoteViews
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import com.relay.app.R

/**
 * Minimal home-screen widget: two buttons (Wake, Stop containers) for the widget's configured
 * default project. Not a project picker — see ARCHITECTURE.md's widget scope ("wake / stop-
 * containers without opening the app").
 */
class RelayWidgetProvider : AppWidgetProvider() {

    override fun onUpdate(context: Context, appWidgetManager: AppWidgetManager, appWidgetIds: IntArray) {
        for (appWidgetId in appWidgetIds) {
            val views = RemoteViews(context.packageName, R.layout.relay_widget)

            views.setOnClickPendingIntent(R.id.widget_wake_button, actionPendingIntent(context, ACTION_WAKE, requestCode = 0))
            views.setOnClickPendingIntent(R.id.widget_stop_button, actionPendingIntent(context, ACTION_STOP_CONTAINERS, requestCode = 1))

            appWidgetManager.updateAppWidget(appWidgetId, views)
        }
    }

    override fun onReceive(context: Context, intent: Intent) {
        super.onReceive(context, intent)
        when (intent.action) {
            ACTION_WAKE -> {
                Log.d(TAG, "Wake tapped — enqueuing WakeRunnerWorker for the widget's default project's runner")
                val request = OneTimeWorkRequestBuilder<WakeRunnerWorker>().build()
                WorkManager.getInstance(context).enqueue(request)
            }
            ACTION_STOP_CONTAINERS -> {
                Log.d(TAG, "Stop tapped — enqueuing StopContainersWorker")
                val request = OneTimeWorkRequestBuilder<StopContainersWorker>().build()
                WorkManager.getInstance(context).enqueue(request)
            }
        }
    }

    private fun actionPendingIntent(context: Context, action: String, requestCode: Int): PendingIntent {
        val intent = Intent(context, RelayWidgetProvider::class.java).apply { this.action = action }
        return PendingIntent.getBroadcast(
            context,
            requestCode,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    companion object {
        private const val TAG = "RelayWidgetProvider"
        const val ACTION_WAKE = "com.relay.app.widget.ACTION_WAKE"
        const val ACTION_STOP_CONTAINERS = "com.relay.app.widget.ACTION_STOP_CONTAINERS"
    }
}
