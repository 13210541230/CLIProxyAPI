"""Exercise live off-period accounting in the owned keep-alive smoke fixture."""
import concurrent.futures
import copy
import json
import sys
sys.stderr = sys.stdout
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from scripts.account_pool_exemption_smoke import (API, CPA, EXEMPT, LOGS, MANAGER, SCRATCH, STUB,
                                                 checked, key_hash, request, wait_until, state as account_state)

smoke = json.loads((LOGS / "account-pool-exemption-smoke.json").read_text())
runtime = checked(MANAGER, "/runtime")
assert runtime.get("managed") and runtime.get("healthy") and not runtime.get("external"), "Not an owned managed fixture"
assert runtime["pid"] == smoke["runtime"]["pid"] and SCRATCH.is_dir(), "Fixture identity changed; refusing control"
base = checked(MANAGER, API)["policy"]
config_path = SCRATCH / "config.yaml"
original_config = config_path.read_text(encoding="utf-8")
ids = smoke["authIds"]


def model_request(key, session, text="probe"):
    return request(CPA, "/v1/responses", "POST", {"model": smoke["model"], "input": [{"role": "user", "content": text}], "stream": False}, key=key, headers={"X-Session-Id": session})
pool = concurrent.futures.ThreadPoolExecutor(max_workers=12)
futures = []


def publish(policy):
    policy = copy.deepcopy(policy)
    policy["version"] = checked(MANAGER, API)["policy"]["version"] + 1
    policy.pop("hash", None)
    checked(MANAGER, API + "/policy", "PUT", {"policy": policy})


def set_enabled(enabled):
    text = original_config if enabled else original_config.replace("      account_pool:\n        enabled: true", "      account_pool:\n        enabled: false")
    config_path.write_text(text, encoding="utf-8")
    wait_until(lambda: checked(MANAGER, API)["status"]["enabled"] == enabled,
               timeout=10, label=f"live pool enabled={enabled}")


try:
    changed = copy.deepcopy(base)
    for binding in changed["bindings"]:
        if binding["apiKeyHash"] == key_hash(EXEMPT):
            binding["crossPoolExempt"] = True
    for member in changed["members"]:
        if member["authId"] == ids["a"]:
            member["enabled"] = False
    publish(changed)
    set_enabled(False)
    checked(STUB, "/block", "POST", {})
    for index in range(12):
        futures.append(pool.submit(model_request, EXEMPT, f"hold-off-{index}", f"hold-off-{index}"))
    physical = wait_until(lambda: (state if (state := checked(STUB, "/state"))["waiting"]["b"] >= 6
                                  and sum(state["waiting"].values()) == 12 else None),
                          label="12 ungated off-period requests reaching real stub")
    observed = {account: account_state(auth) for account, auth in ids.items()}
    for account, state in observed.items():
        assert state["active"] == physical["waiting"][account], (account, state, physical["waiting"])
    set_enabled(True)
    status, body = model_request(EXEMPT, "off-new-probe")
    assert status == 503 and "account_pool_unavailable" in json.dumps(body), (status, body)
    report = {"checks": ["off-period calls bypass limit 5 and all 12 reach stub",
                         "per-account active counters match actual blocked stub calls",
                         "reactivation cannot borrow from physically-full foreign account"],
              "physicalWaiting": physical["waiting"], "observed": observed, "reactivationStatus": status, "reactivationBody": body}
    (LOGS / "account-pool-exemption-off-observation.json").write_text(json.dumps(report, indent=2))
    print(json.dumps(report), flush=True)
finally:
    checked(STUB, "/release", "POST", {})
    for future in futures:
        future.result(timeout=10)
    pool.shutdown(wait=True)
    set_enabled(True)
    publish(base)
    wait_until(lambda: all(account_state(auth)["active"] == 0 for auth in ids.values()),
               label="off-period completions releasing observations")
