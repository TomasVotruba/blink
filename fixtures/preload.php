<?php

declare(strict_types=1);

// used by TestPreload, proves the worker ran this file
file_put_contents((string) getenv('BLINK_PRELOAD_MARKER'), 'preloaded');
