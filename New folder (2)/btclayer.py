import hashlib
from bitcoin import *


# Define the public keys of the 3 parties
pubkeys = [
    'PUBLIC_KEY_1',
    'PUBLIC_KEY_2',
    'PUBLIC_KEY_3'
]

# Create a 2-of-3 multi-signature redeem script
redeem_script = mk_multisig_script(pubkeys, 2)

# Hash the redeem script to get the P2SH address
p2sh_hash = hashlib.sha256(hashlib.new('ripemd160', hashlib.sha256(redeem_script).digest()).digest()).digest()
p2sh_address = b58check_encode(p2sh_hash, version=5)  # version 5 is for P2SH addresses

print(f"Redeem Script: {redeem_script}")
print(f"P2SH Address: {p2sh_address}")

