package com.relay.app.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.relay.app.model.KnownRunner
import com.squareup.moshi.Moshi
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

private val Context.widgetConfigDataStore by preferencesDataStore(name = "widget_config")

/** The runner + project the home-screen widget's Stop-containers button targets. */
data class WidgetTarget(
    val runner: KnownRunner,
    val projectId: String,
)

/**
 * Stores the single "default project" the widget acts on (see task scope: the widget is
 * deliberately not a full project picker). Set from [com.relay.app.ui.screens.ProjectListScreen]
 * via a per-row "set as widget default" action.
 */
class WidgetConfigRepository(context: Context) {

    private val appContext = context.applicationContext
    private val moshi = Moshi.Builder().add(KotlinJsonAdapterFactory()).build()
    private val adapter = moshi.adapter(WidgetTarget::class.java)

    private object Keys {
        val TARGET_JSON = stringPreferencesKey("target_json")
    }

    val defaultTarget: Flow<WidgetTarget?> = appContext.widgetConfigDataStore.data.map { prefs ->
        prefs[Keys.TARGET_JSON]?.let { json -> runCatching { adapter.fromJson(json) }.getOrNull() }
    }

    suspend fun setDefault(target: WidgetTarget) {
        appContext.widgetConfigDataStore.edit { prefs ->
            prefs[Keys.TARGET_JSON] = adapter.toJson(target)
        }
    }
}
