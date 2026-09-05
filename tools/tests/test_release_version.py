import json
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[2]


class ReleaseVersionTests(unittest.TestCase):
    def test_tizen_versions_match_release_source(self):
        version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
        package = json.loads((ROOT / "clients/tv/package.json").read_text(encoding="utf-8"))
        manifest = ET.parse(ROOT / "clients/tizen/config.xml").getroot()
        self.assertRegex(version, r"^[0-9]+\.[0-9]+\.[0-9]+$")
        self.assertEqual(version, package["version"])
        self.assertEqual(version, manifest.attrib["version"])


if __name__ == "__main__":
    unittest.main()
