package com.relay.app.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

private val Context.appPrefsDataStore by preferencesDataStore(name = "app_prefs")

/**
 * Small UI preferences that should survive a restart (DESIGN.md "restore where the user was"):
 * - [currentRunner]: hostname Home shows (the runner switcher's selection).
 * - [lastRoute]: last Home/Project/Chat route, restored on a cold launch by RelayNavHost.
 * - [lastProvider]: provider the last "New chat" used, so the next one needs no dialog.
 * Separate DataStore file from [KnownRunnersRepository] so the runners blob stays untouched.
 */
class AppPrefsRepository(context: Context) {

    private val store = context.applicationContext.appPrefsDataStore

    private object Keys {
        val CURRENT_RUNNER = stringPreferencesKey("current_runner")
        val LAST_ROUTE = stringPreferencesKey("last_route")
        val LAST_PROVIDER = stringPreferencesKey("last_provider")
    }

    val currentRunner: Flow<String?> = store.data.map { it[Keys.CURRENT_RUNNER] }
    val lastRoute: Flow<String?> = store.data.map { it[Keys.LAST_ROUTE] }
    val lastProvider: Flow<String> = store.data.map { it[Keys.LAST_PROVIDER] ?: DEFAULT_PROVIDER }

    suspend fun setCurrentRunner(hostname: String) = store.edit { it[Keys.CURRENT_RUNNER] = hostname }
    suspend fun setLastRoute(route: String) = store.edit { it[Keys.LAST_ROUTE] = route }
    suspend fun setLastProvider(provider: String) = store.edit { it[Keys.LAST_PROVIDER] = provider }

    companion object {
        const val DEFAULT_PROVIDER = "claude"
        /** Providers offered on a long-press of "New chat". */
        val KNOWN_PROVIDERS = listOf("claude", "codex")
    }
}
