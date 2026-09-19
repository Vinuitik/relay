package com.relay.app.data

import kotlin.random.Random

/**
 * Generates a human-readable default display name for a newly paired runner (adjective + noun,
 * e.g. "Silent Falcon") - used when the user leaves the display-name field blank while pairing.
 * Exists because [com.relay.app.model.KnownRunner.hostname] is the Tailscale address/IP used to
 * actually connect (e.g. "100.124.46.7"), never something a person should have to read as a
 * label - see [com.relay.app.model.KnownRunner]'s `displayName` field doc comment.
 */
object FriendlyNameGenerator {
    private val adjectives = listOf(
        "Silent", "Brave", "Swift", "Quiet", "Bright", "Steady", "Calm", "Bold",
        "Lucky", "Clever", "Sunny", "Amber", "Cosmic", "Gentle", "Rapid", "Vivid",
    )
    private val nouns = listOf(
        "Falcon", "Comet", "River", "Forest", "Ember", "Harbor", "Meadow", "Summit",
        "Nebula", "Lantern", "Otter", "Canyon", "Willow", "Beacon", "Glacier", "Orbit",
    )

    fun generate(): String = "${adjectives.random(Random)} ${nouns.random(Random)}"
}
