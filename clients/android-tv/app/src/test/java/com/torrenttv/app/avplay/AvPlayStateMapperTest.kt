package com.torrenttv.app.avplay

import androidx.media3.common.Player
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AvPlayStateMapperTest {
    @Test fun `buffering maps to onbufferingstart`() = assertEquals("onbufferingstart", AvPlayStateMapper.bufferingCallback(Player.STATE_BUFFERING))
    @Test fun `ready maps to onbufferingcomplete`() = assertEquals("onbufferingcomplete", AvPlayStateMapper.bufferingCallback(Player.STATE_READY))
    @Test fun `ended maps to onstreamcompleted`() = assertEquals("onstreamcompleted", AvPlayStateMapper.bufferingCallback(Player.STATE_ENDED))
    @Test fun `idle maps to nothing`() = assertNull(AvPlayStateMapper.bufferingCallback(Player.STATE_IDLE))
}
