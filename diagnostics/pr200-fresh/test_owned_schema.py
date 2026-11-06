import copy
import unittest
import owned_schema as schema


def complete():
    v=schema.placeholder();v.update(childrenExpected=2,omittedRows=0,code='ok',setAgreement='unchanged',complete=True)
    v['rows']=[{'slot':1,'parentSlot':0,'role':'webview','originalHandleVerified':True,'identityBound':True,'before':'live','after':'live','argvStatus':'single','mapping':'exact-runtime-ebwebview-child','directoryStatus':'ok','code':'ok'}, {'slot':2,'parentSlot':1,'role':'console-host','originalHandleVerified':True,'identityBound':True,'before':'live','after':'live','argvStatus':'not-applicable','mapping':'not-applicable','directoryStatus':'not-applicable','code':'ok'}]
    return v


class OwnedSchemaTests(unittest.TestCase):
    def test_exact_child_is_evidence_not_old_gate_clearance(self):
        self.assertTrue(schema.capture_safe(complete())['complete'])
        self.assertFalse(schema.placeholder()['complete'])
        v={'schema':1,'instrumented':True,'captureComplete':True,'oldContinue':False,'scopePassed':False,'originalFailurePreserved':True,'pr200Blocked':True,'status':'observed','persistenceVerified':True,'wrapperCompleted':True,'overlayStable':True}
        self.assertFalse(schema.verdict_safe(v)['scopePassed'])

    def test_unknown_fields_raw_values_or_relaxed_completeness_reject(self):
        mutations=(lambda v:v.update(path='PRIVATE_PATH'),lambda v:v.update(atomicNamespace=True),lambda v:v.update(overrun=True),lambda v:v.update(omittedRows=1),lambda v:v.update(setAgreement='unknown'),lambda v:v['rows'][0].update(slot=2),lambda v:v['rows'][0].update(identityBound=None),lambda v:v['rows'][0].update(after='exited'),lambda v:v['rows'][0].update(mapping='outside'),lambda v:v['rows'][0].update(directoryStatus='directory-alias'),lambda v:v['rows'][1].update(code='not-observed'))
        for mutate in mutations:
            v=copy.deepcopy(complete());mutate(v)
            with self.assertRaises(ValueError):schema.capture_safe(v)

    def test_partial_rows_are_retained_without_completeness(self):
        v=complete();v.update(complete=False,code='exited-during');v['rows'][1].update(after='exited',code='exited-during')
        self.assertEqual(len(schema.capture_safe(v)['rows']),2)
