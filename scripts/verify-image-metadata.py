"""Verify executable and OCI metadata before publishing a container image."""
import argparse
import json
import subprocess


def main():
    parser=argparse.ArgumentParser()
    for name in ("image","version","revision","created"): parser.add_argument("--"+name,required=True)
    parser.add_argument("--source",default="https://github.com/jay739/omnifin")
    args=parser.parse_args()
    data=json.loads(subprocess.check_output(["docker","run","--rm","--network","none","--read-only","--entrypoint","/jfa-go/omnifin",args.image,"-version"],text=True))
    image=json.loads(subprocess.check_output(["docker","image","inspect",args.image],text=True))[0]
    labels=image["Config"].get("Labels") or {}
    expected={"version":args.version,"commit":args.revision,"build_mode":"docker","build_time":args.created}
    for key,value in expected.items():
        if data.get(key)!=value: raise SystemExit("Executable build metadata does not match release input: "+key)
    for key,value in {"version":args.version,"revision":args.revision,"created":args.created,"source":args.source}.items():
        if labels.get("org.opencontainers.image."+key)!=value: raise SystemExit("OCI metadata does not match release input: "+key)
    print("Executable and OCI metadata agree on version, full revision, build time and mode.")


if __name__=="__main__": main()
