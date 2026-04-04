#pragma once

#ifdef __cplusplus
extern "C" {
#endif

struct SharedPayload {
    int value;
};

int shared_c_function(int value);

#ifdef __cplusplus
}
#endif
