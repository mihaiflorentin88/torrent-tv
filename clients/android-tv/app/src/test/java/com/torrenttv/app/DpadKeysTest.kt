package com.torrenttv.app

import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DpadKeysTest {
    @Test fun `down scripts map every dpad key to the DOM events the page consumes`() {
        assertTrue(DpadKeys.downScript(21)!!.contains("ArrowLeft") && DpadKeys.downScript(21)!!.contains("37"))
        assertTrue(DpadKeys.downScript(22)!!.contains("ArrowRight") && DpadKeys.downScript(22)!!.contains("39"))
        assertTrue(DpadKeys.downScript(19)!!.contains("ArrowUp") && DpadKeys.downScript(19)!!.contains("38"))
        assertTrue(DpadKeys.downScript(20)!!.contains("ArrowDown") && DpadKeys.downScript(20)!!.contains("40"))
        assertTrue(DpadKeys.downScript(23)!!.contains("Enter") && DpadKeys.downScript(23)!!.contains("13"))
        assertTrue(DpadKeys.downScript(66)!!.contains("Enter") && DpadKeys.downScript(66)!!.contains("13"))
    }
    @Test fun `up scripts dispatch the matching keyup`() {
        assertTrue(DpadKeys.upScript(20)!!.contains("keyup"))
        assertTrue(DpadKeys.upScript(20)!!.contains("ArrowDown"))
    }
    @Test fun `keys outside the d-pad set are left for the WebView`() {
        assertNull(DpadKeys.downScript(4))
        assertNull(DpadKeys.upScript(24))
    }
}
