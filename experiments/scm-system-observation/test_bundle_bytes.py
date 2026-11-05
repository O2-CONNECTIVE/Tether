import io,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
from bundle_bytes import write_utf8_lf
class MetadataByteContract(unittest.TestCase):
 def test_windows_text_default_cannot_change_approved_packet_bytes(self):
  original=io.open
  def windows_open(file,mode='r',buffering=-1,encoding=None,errors=None,newline=None,closefd=True,opener=None):
   if 'b'not in mode and ('w'in mode or 'a'in mode) and newline is None:newline='\r\n'
   return original(file,mode,buffering,encoding,errors,newline,closefd,opener)
  with tempfile.TemporaryDirectory()as d:
   p=Path(d)/'manifest.json'
   with patch('io.open',windows_open):write_utf8_lf(p,'{"schema": 1}\n')
   self.assertEqual(p.read_bytes(),b'{"schema": 1}\n')
