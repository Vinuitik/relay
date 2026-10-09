package com.relay.app.ui.screens

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.relay.app.data.ScheduleRepository
import com.relay.app.model.Booking
import com.relay.app.model.KnownRunner
import com.relay.app.network.friendlyErrorMessage
import com.squareup.moshi.Moshi
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import retrofit2.HttpException
import java.time.LocalDate

/**
 * Booking editor state. Mode comes from the route: `date` only = new, `bookingId` + `date` = an
 * occurrence of an existing booking, `bookingId` only = the whole booking. An existing booking is
 * loaded from `ScheduleRepository.refresh(runner).bookings` (there's no GET-by-id endpoint).
 *
 * Saves/deletes go through [ScheduleRepository], which re-fetches the cache the grid reads.
 * 400/409 → [serverError] (inline, the runner's own `error` text); anything else → [snackbar].
 */
class BookingEditorViewModel(
    private val runner: KnownRunner,
    private val repo: ScheduleRepository,
    val bookingId: String?,
    /** Route `date`: the day tapped (new) or the occurrence's original date. */
    val routeDate: String?,
) : ViewModel() {

    val mode: EditorMode = when {
        bookingId == null -> EditorMode.NEW
        routeDate != null -> EditorMode.OCCURRENCE
        else -> EditorMode.SERIES
    }

    var loading by mutableStateOf(bookingId != null)
        private set
    /** Couldn't load the booking (or it's gone) - nothing to edit. */
    var loadError by mutableStateOf<String?>(null)
        private set
    var booking by mutableStateOf<Booking?>(null)
        private set
    var form by mutableStateOf(BookingForm(date = LocalDate.now()))
        private set
    /** As loaded - to tell whether title/repeat were touched (those can't be "this day only"). */
    private var original: BookingForm? = null

    var saving by mutableStateOf(false)
        private set
    /** Runner's 400/409 `error` text, shown inline above Save. */
    var serverError by mutableStateOf<String?>(null)
        private set
    /** One-shot network error for a snackbar; the screen clears it with [snackbarShown]. */
    var snackbar by mutableStateOf<String?>(null)
        private set
    /** Saved or deleted: the screen pops back to the grid. */
    var done by mutableStateOf(false)
        private set

    val errors: FormErrors get() = validateForm(form, includeRepeat = true)
    val isSeries: Boolean get() = booking?.repeat != null
    /** Opened on one day of a repeating booking: save/delete ask "this day or the whole series". */
    val asksScope: Boolean get() = mode == EditorMode.OCCURRENCE && isSeries
    /** Title/repeat changed: only the whole series can take that (a Day has neither). */
    val seriesOnlyChanges: Boolean
        get() = original?.let { it.title.trim() != form.title.trim() || it.repeatPart() != form.repeatPart() } ?: false
    /** The day the preview / "this day" refers to. */
    val occurrenceDate: LocalDate?
        get() = routeDate?.let { runCatching { LocalDate.parse(it) }.getOrNull() }

    init {
        if (bookingId == null) {
            viewModelScope.launch {
                val tz = runCatching { repo.cached(runner).first()?.timezone }.getOrNull()
                val date = occurrenceDate ?: LocalDate.now(zoneOf(tz))
                form = BookingForm(date = date)
            }
        } else {
            load()
        }
    }

    fun load() {
        val id = bookingId ?: return
        loading = true
        loadError = null
        viewModelScope.launch {
            try {
                val b = repo.refresh(runner).bookings.firstOrNull { it.id == id }
                if (b == null) {
                    loadError = "This booking no longer exists."
                } else {
                    booking = b
                    val f = formFromBooking(b, if (mode == EditorMode.OCCURRENCE) routeDate else null)
                    original = f
                    form = f
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                loadError = friendlyErrorMessage(e, runner)
            } finally {
                loading = false
            }
        }
    }

    fun update(transform: (BookingForm) -> BookingForm) {
        val next = transform(form)
        form = next.copy(sleeps = sortSleeps(next.sleeps))
        serverError = null
    }

    fun addSleep() {
        val proposal = proposeSleep(form.start, form.end, form.sleeps) ?: return
        update { it.copy(sleeps = it.sleeps + proposal) }
    }

    fun snackbarShown() { snackbar = null }

    /** New → create; one-off / whole series → PUT series; [thisDayOnly] → PUT occurrence. */
    fun save(thisDayOnly: Boolean) {
        val f = form
        val valid = validateForm(f, includeRepeat = !thisDayOnly).isValid
        if (!valid || saving) return
        mutate {
            when {
                bookingId == null -> repo.createBooking(runner, f.toBookingInput())
                thisDayOnly -> repo.updateOccurrence(runner, bookingId, routeDate!!, f.toDay())
                else -> repo.updateSeries(runner, bookingId, f.toBookingInput())
            }
        }
    }

    /** [thisDayOnly] → cancel that occurrence; else delete the whole booking. */
    fun delete(thisDayOnly: Boolean) {
        val id = bookingId ?: return
        if (saving) return
        mutate {
            if (thisDayOnly) repo.cancelOccurrence(runner, id, routeDate!!) else repo.deleteSeries(runner, id)
        }
    }

    private fun mutate(block: suspend () -> Unit) {
        saving = true
        serverError = null
        viewModelScope.launch {
            try {
                block()
                done = true
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                val inline = (e as? HttpException)?.takeIf { it.code() == 400 || it.code() == 409 }
                if (inline != null) {
                    val reason = runCatching {
                        errorAdapter.fromJson(inline.response()?.errorBody()?.string().orEmpty())?.error
                    }.getOrNull()
                    serverError = when {
                        !reason.isNullOrBlank() -> reason
                        inline.code() == 409 -> "Overlaps another booking."
                        else -> "The runner rejected this booking."
                    }
                } else {
                    snackbar = friendlyErrorMessage(e, runner)
                }
            } finally {
                saving = false
            }
        }
    }

    private data class ErrorBody(val error: String?)

    private companion object {
        val errorAdapter = Moshi.Builder().add(KotlinJsonAdapterFactory()).build().adapter(ErrorBody::class.java)
    }
}
