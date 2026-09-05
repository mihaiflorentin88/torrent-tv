package com.torrenttv.app

import org.junit.Assert.assertEquals
import org.junit.Test

class SubnetMaskTest {
    @Test fun `prefix 24 renders the familiar mask`() = assertEquals("255.255.255.0", SubnetMask.forPrefix(24))
    @Test fun `prefix 16 renders a class B mask`() = assertEquals("255.255.0.0", SubnetMask.forPrefix(16))
    @Test fun `prefix 32 is the host mask`() = assertEquals("255.255.255.255", SubnetMask.forPrefix(32))
    @Test fun `prefix 0 is the zero mask`() = assertEquals("0.0.0.0", SubnetMask.forPrefix(0))
    @Test fun `out of range prefixes clamp instead of throwing`() {
        assertEquals("255.255.255.255", SubnetMask.forPrefix(99))
        assertEquals("0.0.0.0", SubnetMask.forPrefix(-3))
    }
}
