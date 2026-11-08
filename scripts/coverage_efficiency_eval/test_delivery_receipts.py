"""普通任务允许新建后编辑，验收须检查完整版本链而非只准一次写入。"""
import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("delivery_v2_runner", Path(__file__).with_name("run.py"))
wrapper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wrapper)
r = wrapper.r


class DeliveryReceiptTests(unittest.TestCase):
    def chain(self):
        return [
            {"action_id": "create", "tool": "write_file", "status": "SUCCEEDED", "effect": "CONFIRMED", "file": {"path": r.OUTPUT, "before_version": "missing", "after_version": "a" * 64}},
            {"action_id": "edit", "tool": "edit_file", "status": "SUCCEEDED", "effect": "CONFIRMED", "file": {"path": r.OUTPUT, "before_version": "a" * 64, "after_version": "b" * 64}},
        ]

    def test_confirmed_create_edit_and_no_change_form_valid_delivery(self):
        chain = self.chain()
        self.assertTrue(r.receipt_chain_valid(chain, r.OUTPUT, "b" * 64))
        chain.append(dict(chain[-1], action_id="unchanged", file={"path":r.OUTPUT,"before_version":"b"*64,"after_version":"b"*64}))
        self.assertTrue(r.receipt_chain_valid(chain, r.OUTPUT, "b" * 64))

    def test_unknown_discontinuity_duplicate_identity_and_wrong_final_are_rejected(self):
        chain = self.chain()
        for mutate in [
            lambda c:c[-1].update(effect="UNKNOWN"),
            lambda c:c[-1]["file"].update(before_version="c"*64),
            lambda c:c[-1].update(action_id="create"),
            lambda c:c[-1].update(status="REJECTED"),
        ]:
            changed = copy.deepcopy(chain)
            mutate(changed)
            self.assertFalse(r.receipt_chain_valid(changed, r.OUTPUT, "b" * 64))
        self.assertFalse(r.receipt_chain_valid(chain,r.OUTPUT,"c"*64))
        self.assertFalse(r.receipt_chain_valid([],r.OUTPUT,"b"*64))

    def test_other_paths_cannot_supply_missing_target_versions(self):
        chain = self.chain()
        chain[0]["file"]["path"] = "other.md"
        self.assertFalse(r.receipt_chain_valid(chain,r.OUTPUT,"b"*64))
