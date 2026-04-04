#pragma once

#include "qt_stubs.h"

#if defined(MYLIB_LIBRARY)
#define MY_EXPORT Q_DECL_EXPORT
#else
#define MY_EXPORT Q_DECL_IMPORT
#endif
