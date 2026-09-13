package com.relay.app.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.relay.app.model.KnownRunner
import com.squareup.moshi.Moshi
import com.squareup.moshi.Types
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

private val Context.knownRunnersDataStore by preferencesDataStore(name = "known_runners")

/**
 * Persists the manually-entered known-runners list (hostname + key per runner, see
 * ARCHITECTURE.md "Registration") via Jetpack DataStore. Stored as one JSON blob under a single
 * preferences key rather than a typed proto schema — the list is small and its shape is simple
 * enough that Preferences DataStore + Moshi is less ceremony than DataStore-proto for a skeleton.
 */
class KnownRunnersRepository(context: Context) {

    private val appContext = context.applicationContext

    private val moshi = Moshi.Builder().add(KotlinJsonAdapterFactory()).build()
    private val listType = Types.newParameterizedType(List::class.java, KnownRunner::class.java)
    private val adapter = moshi.adapter<List<KnownRunner>>(listType)

    private object Keys {
        val RUNNERS_JSON = stringPreferencesKey("runners_json")
    }

    val runners: Flow<List<KnownRunner>> = appContext.knownRunnersDataStore.data.map { prefs ->
        decode(prefs[Keys.RUNNERS_JSON])
    }

    suspend fun addRunner(runner: KnownRunner) {
        appContext.knownRunnersDataStore.edit { prefs ->
            val current = decode(prefs[Keys.RUNNERS_JSON])
            val updated = current.filterNot { it.hostname == runner.hostname } + runner
            prefs[Keys.RUNNERS_JSON] = adapter.toJson(updated)
        }
    }

    suspend fun removeRunner(hostname: String) {
        appContext.knownRunnersDataStore.edit { prefs ->
            val current = decode(prefs[Keys.RUNNERS_JSON])
            prefs[Keys.RUNNERS_JSON] = adapter.toJson(current.filterNot { it.hostname == hostname })
        }
    }

    private fun decode(json: String?): List<KnownRunner> {
        if (json.isNullOrBlank()) return emptyList()
        return runCatching { adapter.fromJson(json) }.getOrNull() ?: emptyList()
    }
}
