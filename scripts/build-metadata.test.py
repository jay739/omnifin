import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]


class BuildMetadataTests(unittest.TestCase):
    def capture(self, extra):
        with tempfile.TemporaryDirectory(prefix="omnifin-metadata-") as folder:
            capture = Path(folder) / "capture.py"
            capture.write_text("#!/usr/bin/env python3\nimport json,os,sys\nprint(json.dumps({'env':{k:os.environ.get(k) for k in ['JFA_GO_VERSION','JFA_GO_COMMIT','JFA_GO_BUILD_TIME','JFA_GO_CSS_VERSION','JFA_GO_BUILD_MODE','JFA_GO_NFPM_EPOCH']},'args':sys.argv[1:]}))\n")
            capture.chmod(0o755)
            env = {k:v for k,v in os.environ.items() if not k.startswith("JFA_GO_")}
            env.update(extra)
            return subprocess.run(["bash", str(REPO / "scripts/version.sh"), str(capture), "argument with spaces"], cwd=folder, env=env, text=True, capture_output=True, timeout=15)

    def test_explicit_metadata_without_git(self):
        result = self.capture({"JFA_GO_VERSION":"v1.4.3-rc1", "JFA_GO_COMMIT":"a"*40, "JFA_GO_BUILD_TIME":"1800000000", "JFA_GO_CSS_VERSION":"v1.4.3-rc1", "JFA_GO_BUILD_MODE":"docker", "JFA_GO_NFPM_EPOCH":"42"})
        self.assertEqual(result.returncode,0,result.stderr)
        data = json.loads(result.stdout)
        self.assertEqual(data["env"]["JFA_GO_VERSION"],"1.4.3-rc1")
        self.assertEqual(data["env"]["JFA_GO_COMMIT"],"a"*40)
        self.assertEqual(data["env"]["JFA_GO_BUILD_TIME"],"1800000000")
        self.assertEqual(data["env"]["JFA_GO_CSS_VERSION"],"v1.4.3-rc1")
        self.assertEqual(data["args"],["argument with spaces","--timeout","60m"])

    def test_container_build_requires_revision(self):
        result = self.capture({"JFA_GO_VERSION":"v1.4.3","JFA_GO_BUILD_MODE":"docker"})
        self.assertNotEqual(result.returncode,0)

    def test_development_without_git_is_identified(self):
        result = self.capture({})
        self.assertEqual(result.returncode,0,result.stderr)
        data = json.loads(result.stdout)["env"]
        self.assertEqual(data["JFA_GO_VERSION"],"development")
        self.assertEqual(data["JFA_GO_COMMIT"],"unknown")
        self.assertTrue(data["JFA_GO_BUILD_TIME"].isdigit())

    def test_compiled_binary_identifies_full_source_revision(self):
        binary=REPO / "build/omnifin"
        if not binary.exists(): self.skipTest("run make all before checking the compiled binary")
        result=subprocess.run([str(binary),"-version"],capture_output=True,text=True,timeout=10)
        self.assertEqual(result.returncode,0,result.stderr)
        info=json.loads(result.stdout)
        revision=subprocess.check_output(["git","rev-parse","HEAD"],cwd=REPO,text=True).strip()
        self.assertEqual(info["commit"],revision)
        self.assertEqual(info["build_mode"],"binary")
        self.assertTrue(info["version"])
        self.assertTrue(info["build_time"].endswith("Z"))


if __name__ == "__main__":
    unittest.main()
