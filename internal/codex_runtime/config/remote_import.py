# -*- coding: utf-8 -*-
"""Optional callback that hands completed registrations to Space Console."""
from config.env_loader import apply_env_overrides

REMOTE_IMPORT_ENABLED: bool = True
REMOTE_IMPORT_URL: str = "http://127.0.0.1:18120/api/integrations/turb/register"
REMOTE_IMPORT_USERNAME: str = "admin"
REMOTE_IMPORT_PASSWORD: str = "admin"
REMOTE_IMPORT_TIMEOUT: int = 20

apply_env_overrides(globals(), {"REMOTE_IMPORT_ENABLED": "bool", "REMOTE_IMPORT_URL": "str", "REMOTE_IMPORT_USERNAME": "str", "REMOTE_IMPORT_PASSWORD": "str", "REMOTE_IMPORT_TIMEOUT": "int"})
