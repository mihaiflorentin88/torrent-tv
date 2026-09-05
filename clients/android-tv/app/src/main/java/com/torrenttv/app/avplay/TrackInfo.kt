package com.torrenttv.app.avplay

import org.json.JSONArray
import org.json.JSONObject

/**
 * Serializes tracks into the shape `normalizeTrack` in the web app parses:
 * a JSON array of { index, type, extra_info }, where extra_info is itself a
 * JSON string carrying track_lang and codec.
 */
object TrackInfo {
    data class Track(val type: String, val language: String, val codec: String)

    fun toJson(tracks: List<Track>): String {
        val array = JSONArray()
        tracks.forEachIndexed { index, track ->
            val extra = JSONObject()
            extra.put("track_lang", track.language)
            if (track.codec.isNotEmpty()) extra.put("codec", track.codec)
            array.put(JSONObject().put("index", index).put("type", track.type).put("extra_info", extra.toString()))
        }
        return array.toString()
    }
}
