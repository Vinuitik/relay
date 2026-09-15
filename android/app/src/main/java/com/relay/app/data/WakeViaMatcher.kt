package com.relay.app.data

import android.util.Log
import com.relay.app.model.KnownRunner
import com.relay.app.network.RelayApiClient
import kotlinx.coroutines.flow.first

/**
 * Auto-fills [KnownRunner.wakeViaRunnerId] by matching `GET /v1/runner/info`'s `localSubnet`
 * across known runners, instead of requiring the manual "Edit" step on RunnerListScreen for that
 * field — see ARCHITECTURE.md "Relay device" and runner/FLOWS.md "Same-LAN detection for
 * wake-via auto-match".
 *
 * Deliberately simple, no persistence of its own, no retry queue: called once right after a
 * runner is added, best-effort, "if it doesn't match, the manual Edit affordance is still there."
 * Exactly one already-known, reachable runner sharing the new runner's subnet is required for a
 * match — zero or multiple candidates leave both runners exactly as they were (never guesses).
 */
object WakeViaMatcher {

    private const val TAG = "WakeViaMatcher"

    /**
     * Attempts to auto-set `wakeViaRunnerId` for [newRunner] against every other runner already
     * in [repository], and (symmetrically) the matched runner's own `wakeViaRunnerId` back to
     * [newRunner] - either one might be the one that's asleep later, so both directions are
     * useful. Returns the matched runner's hostname, or null if no auto-match was made.
     */
    suspend fun autoMatch(repository: KnownRunnersRepository, newRunner: KnownRunner): String? {
        val others = repository.runners.first().filter { it.hostname != newRunner.hostname }
        if (others.isEmpty()) return null

        val newSubnet = subnetOf(newRunner)
        if (newSubnet == null) {
            Log.d(TAG, "Could not read localSubnet from ${newRunner.hostname}, skipping auto-match")
            return null
        }

        val matches = others.filter { subnetOf(it) == newSubnet }
        if (matches.size != 1) {
            Log.d(TAG, "${matches.size} runner(s) share ${newRunner.hostname}'s subnet, want exactly 1 - skipping auto-match")
            return null
        }

        val matched = matches.single()
        repository.updateRunner(newRunner.copy(wakeViaRunnerId = matched.hostname))
        repository.updateRunner(matched.copy(wakeViaRunnerId = newRunner.hostname))
        Log.d(TAG, "Auto-matched wake-via: ${newRunner.hostname} <-> ${matched.hostname}")
        return matched.hostname
    }

    private suspend fun subnetOf(runner: KnownRunner): String? {
        return try {
            RelayApiClient.forRunner(runner).runnerInfo().localSubnet?.takeIf { it.isNotBlank() }
        } catch (e: Exception) {
            Log.d(TAG, "Could not reach ${runner.hostname} for subnet check: ${e.message}")
            null
        }
    }
}
