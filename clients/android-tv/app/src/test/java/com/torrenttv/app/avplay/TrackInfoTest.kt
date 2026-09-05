package com.torrenttv.app.avplay

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class TrackInfoTest {
    @Test fun `serializes to the AVPlay total-track-info shape`() {
        val json = TrackInfo.toJson(listOf(
            TrackInfo.Track(type = "AUDIO", language = "eng", codec = "audio/eac3"),
            TrackInfo.Track(type = "TEXT", language = "ron", codec = "text/vtt"),
        ))
        val parsed = org.json.JSONArray(json)
        assertEquals(2, parsed.length())
        val audio = parsed.getJSONObject(0)
        assertEquals("AUDIO", audio.getString("type"))
        assertEquals(0, audio.getInt("index"))
        val extra = org.json.JSONObject(audio.getString("extra_info"))
        assertEquals("eng", extra.getString("track_lang"))
        assertEquals("audio/eac3", extra.optString("codec"))
        val text = parsed.getJSONObject(1)
        assertEquals("TEXT", text.getString("type"))
        assertEquals(1, text.getInt("index"))
        assertTrue(text.getString("extra_info").contains("ron"))
    }
    @Test fun `empty track list serializes to an empty array`() = assertEquals("[]", TrackInfo.toJson(emptyList()))
}
